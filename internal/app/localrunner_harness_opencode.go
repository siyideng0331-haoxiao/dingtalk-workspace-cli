// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	localRunnerOpenCodeUsername = "opencode"
	localRunnerOpenCodeHealthPath = "/global/health"
	localRunnerOpenCodeStartupTimeout = 20 * time.Second
	localRunnerOpenCodePollInterval = 150 * time.Millisecond
)

var errLocalRunnerOpenCodeSessionMissing = errors.New("opencode session missing")

type localRunnerOpenCodeTransport struct {
	options localRunnerHarnessOptions
	sessions *localRunnerHarnessSessions
	server *localRunnerOpenCodeServer
}

type localRunnerOpenCodeServer struct {
	options localRunnerHarnessOptions
	mu sync.Mutex
	cmd *exec.Cmd
	done chan error
	baseURL string
	password string
	client *http.Client
	output localRunnerHarnessLockedBuffer
}

type localRunnerOpenCodeClient struct {
	baseURL string
	password string
	client *http.Client
}

type localRunnerOpenCodePart struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Delta string `json:"delta"`
	Parts []localRunnerOpenCodePart `json:"parts"`
	Data json.RawMessage `json:"data"`
}

type localRunnerOpenCodeMessageRecord struct {
	Info struct {
		ID string `json:"id"`
		Role string `json:"role"`
		ParentID string `json:"parentID"`
		Time struct {
			Completed *int64 `json:"completed"`
		} `json:"time"`
		Error json.RawMessage `json:"error"`
	} `json:"info"`
	Parts []localRunnerOpenCodePart `json:"parts"`
}

func newLocalRunnerOpenCodeTransport(options localRunnerHarnessOptions) *localRunnerOpenCodeTransport {
	return &localRunnerOpenCodeTransport{
		options: options,
		sessions: newLocalRunnerHarnessSessions(options.stateKey, "opencode-sessions.json", options.memory),
		server: &localRunnerOpenCodeServer{options: options, client: &http.Client{}},
	}
}

func (t *localRunnerOpenCodeTransport) Warm(ctx context.Context) error {
	_, err := t.server.ensure(ctx)
	return err
}

func (t *localRunnerOpenCodeTransport) Prompt(ctx context.Context, contextID, prompt string, onDelta func(string)) (string, error) {
	ctx, cancel := localRunnerHarnessContext(ctx, t.options.timeout)
	defer cancel()
	client, err := t.server.ensure(ctx)
	if err != nil {
		return "", err
	}
	sessionID := ""
	if t.options.memory {
		sessionID = t.sessions.Get(contextID)
	}
	if sessionID == "" {
		sessionID, err = client.createSession(ctx)
		if err != nil {
			return "", err
		}
		if t.options.memory {
			t.sessions.Set(contextID, sessionID)
		}
	}
	reply := ""
	if onDelta == nil {
		reply, err = client.sendMessage(ctx, sessionID, prompt, t.options.model)
	} else {
		reply, err = client.streamMessage(ctx, sessionID, prompt, t.options.model, onDelta)
	}
	if errors.Is(err, errLocalRunnerOpenCodeSessionMissing) && t.options.memory {
		t.sessions.Set(contextID, "")
		sessionID, err = client.createSession(ctx)
		if err == nil {
			t.sessions.Set(contextID, sessionID)
			if onDelta == nil {
				reply, err = client.sendMessage(ctx, sessionID, prompt, t.options.model)
			} else {
				reply, err = client.streamMessage(ctx, sessionID, prompt, t.options.model, onDelta)
			}
		}
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(reply) == "" {
		return "", errorsNewLocalRunnerEmptyReply("OpenCode")
	}
	return reply, nil
}

func (t *localRunnerOpenCodeTransport) Close() error {
	return t.server.Close()
}

func (s *localRunnerOpenCodeServer) ensure(ctx context.Context) (*localRunnerOpenCodeClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil {
		s.client = &http.Client{}
	}
	client := &localRunnerOpenCodeClient{baseURL: s.baseURL, password: s.password, client: s.client}
	if s.baseURL != "" && client.health(ctx) == nil {
		return client, nil
	}
	if s.cmd != nil {
		_ = s.closeLocked()
	}
	port, err := localRunnerFreePort()
	if err != nil {
		return nil, err
	}
	password := localRunnerRandomHex(24)
	args := append([]string{}, s.options.command[1:]...)
	args = append(args, "serve", "--pure", "--hostname", "127.0.0.1", "--port", fmt.Sprint(port))
	cmd := exec.Command(s.options.bin, args...)
	cmd.Dir = s.options.workDir
	cmd.Env = localRunnerOpenCodeEnv(password, s.options.accessMode, s.options.configDir)
	s.output.Reset()
	cmd.Stdout = &s.output
	cmd.Stderr = &s.output
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start opencode serve: %w", err)
	}
	s.cmd = cmd
	s.done = make(chan error, 1)
	s.baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	s.password = password
	go func() { s.done <- cmd.Wait() }()
	client = &localRunnerOpenCodeClient{baseURL: s.baseURL, password: s.password, client: s.client}
	deadline := time.NewTimer(localRunnerOpenCodeStartupTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(localRunnerOpenCodePollInterval)
	defer ticker.Stop()
	for {
		if err := client.health(ctx); err == nil {
			return client, nil
		}
		select {
		case processErr := <-s.done:
			detail := localRunnerHarnessProcessDetail(processErr, s.output.String())
			s.clearLocked()
			return nil, errorsNewLocalRunnerProcessExit("OpenCode serve", detail)
		case <-ticker.C:
		case <-deadline.C:
			_ = s.closeLocked()
			return nil, fmt.Errorf("wait for opencode serve health timed out")
		case <-ctx.Done():
			_ = s.closeLocked()
			return nil, ctx.Err()
		}
	}
}

func (s *localRunnerOpenCodeServer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeLocked()
}

func (s *localRunnerOpenCodeServer) closeLocked() error {
	if s.cmd == nil || s.cmd.Process == nil {
		s.clearLocked()
		return nil
	}
	err := s.cmd.Process.Kill()
	if s.done != nil {
		select {
		case <-s.done:
		case <-time.After(2 * time.Second):
		}
	}
	s.clearLocked()
	return err
}

func (s *localRunnerOpenCodeServer) clearLocked() {
	s.cmd = nil
	s.done = nil
	s.baseURL = ""
	s.password = ""
}

func (c *localRunnerOpenCodeClient) health(ctx context.Context) error {
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var response struct {
		Healthy bool `json:"healthy"`
	}
	if err := c.doJSON(probe, http.MethodGet, localRunnerOpenCodeHealthPath, nil, &response); err != nil {
		return err
	}
	if !response.Healthy {
		return fmt.Errorf("opencode serve health=false")
	}
	return nil
}

func (c *localRunnerOpenCodeClient) createSession(ctx context.Context) (string, error) {
	var response struct {
		ID string `json:"id"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/session", map[string]any{}, &response); err != nil {
		return "", err
	}
	if strings.TrimSpace(response.ID) == "" {
		return "", fmt.Errorf("opencode response missing session id")
	}
	return response.ID, nil
}

func (c *localRunnerOpenCodeClient) sendMessage(ctx context.Context, sessionID, prompt, model string) (string, error) {
	body := localRunnerOpenCodeMessageBody(prompt, model)
	var response struct {
		Parts []localRunnerOpenCodePart `json:"parts"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/session/"+sessionID+"/message", body, &response); err != nil {
		return "", err
	}
	return strings.TrimSpace(localRunnerOpenCodePartsText(response.Parts)), nil
}

func localRunnerOpenCodeMessageBody(prompt, model string) map[string]any {
	body := map[string]any{"parts": []map[string]string{{"type": "text", "text": prompt}}}
	if strings.TrimSpace(model) != "" {
		provider, modelID, found := strings.Cut(model, "/")
		if found && strings.TrimSpace(provider) != "" && strings.TrimSpace(modelID) != "" {
			body["model"] = map[string]string{"providerID": provider, "modelID": modelID}
		} else {
			body["model"] = map[string]string{"modelID": model}
		}
	}
	return body
}

func (c *localRunnerOpenCodeClient) streamMessage(ctx context.Context, sessionID, prompt, model string, onDelta func(string)) (string, error) {
	body := localRunnerOpenCodeMessageBody(prompt, model)
	userMessageID := "msg_" + localRunnerRandomHex(12)
	body["messageID"] = userMessageID
	if err := c.doJSON(ctx, http.MethodPost, "/session/"+sessionID+"/prompt_async", body, nil); err != nil {
		return "", err
	}
	completed := false
	defer func() {
		if completed {
			return
		}
		abortContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.doJSON(abortContext, http.MethodPost, "/session/"+sessionID+"/abort", nil, nil)
	}()
	ticker := time.NewTicker(localRunnerOpenCodePollInterval)
	defer ticker.Stop()
	lastSnapshot := ""
	seenBusy := false
	for {
		snapshot, assistantCompleted, found, err := c.assistantSnapshot(ctx, sessionID, userMessageID)
		if err != nil {
			return "", err
		}
		if snapshot != "" && snapshot != lastSnapshot {
			lastSnapshot = snapshot
			onDelta(snapshot)
		}
		busy, present, err := c.sessionBusy(ctx, sessionID)
		if err != nil {
			return "", err
		}
		seenBusy = seenBusy || busy
		if found && assistantCompleted {
			completed = true
			return lastSnapshot, nil
		}
		if found && seenBusy && !present && strings.TrimSpace(lastSnapshot) != "" {
			completed = true
			return lastSnapshot, nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func (c *localRunnerOpenCodeClient) assistantSnapshot(ctx context.Context, sessionID, userMessageID string) (string, bool, bool, error) {
	var messages []localRunnerOpenCodeMessageRecord
	if err := c.doJSON(ctx, http.MethodGet, "/session/"+sessionID+"/message?limit=100", nil, &messages); err != nil {
		return "", false, false, err
	}
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Info.Role != "assistant" || message.Info.ParentID != userMessageID {
			continue
		}
		if len(message.Info.Error) > 0 && string(message.Info.Error) != "null" {
			return "", false, true, fmt.Errorf("opencode assistant request failed")
		}
		return strings.TrimSpace(localRunnerOpenCodePartsText(message.Parts)), message.Info.Time.Completed != nil, true, nil
	}
	return "", false, false, nil
}

func (c *localRunnerOpenCodeClient) sessionBusy(ctx context.Context, sessionID string) (bool, bool, error) {
	var statuses map[string]struct {
		Type string `json:"type"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/session/status", nil, &statuses); err != nil {
		return false, false, err
	}
	status, present := statuses[sessionID]
	return present && status.Type == "busy", present, nil
}

func (c *localRunnerOpenCodeClient) doJSON(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.baseURL, "/")+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.password != "" {
		request.SetBasicAuth(localRunnerOpenCodeUsername, c.password)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound && strings.HasPrefix(path, "/session/") {
		return errLocalRunnerOpenCodeSessionMissing
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		detail := localRunnerHarnessSafeDetail(string(raw), nil)
		if detail == "" {
			detail = response.Status
		}
		return fmt.Errorf("opencode server %s %s failed: %s", method, path, detail)
	}
	if output == nil {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func localRunnerOpenCodePartsText(parts []localRunnerOpenCodePart) string {
	var result strings.Builder
	var walk func([]localRunnerOpenCodePart)
	walk = func(items []localRunnerOpenCodePart) {
		for _, item := range items {
			if item.Type == "text" {
				result.WriteString(item.Text)
				result.WriteString(item.Delta)
				if len(item.Data) > 0 {
					var nested struct {
						Text string `json:"text"`
					}
					if json.Unmarshal(item.Data, &nested) == nil {
						result.WriteString(nested.Text)
					}
				}
			}
			walk(item.Parts)
		}
	}
	walk(parts)
	return result.String()
}

func localRunnerFreePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func localRunnerRandomHex(length int) string {
	value := make([]byte, length)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprint(time.Now().UnixNano())
	}
	return hex.EncodeToString(value)
}

func localRunnerOpenCodeEnv(password, accessMode, configDir string) []string {
	environment := append([]string{}, os.Environ()...)
	environment = localRunnerUpsertEnv(environment, "OPENCODE_SERVER_USERNAME", localRunnerOpenCodeUsername)
	environment = localRunnerUpsertEnv(environment, "OPENCODE_SERVER_PASSWORD", password)
	configuration := localRunnerOpenCodeConfig(localRunnerEnvironmentValue(environment, "OPENCODE_CONFIG_CONTENT"), accessMode, configDir)
	environment = localRunnerUpsertEnv(environment, "OPENCODE_CONFIG_CONTENT", configuration)
	var decoded map[string]any
	_ = json.Unmarshal([]byte(configuration), &decoded)
	permission, _ := json.Marshal(decoded["permission"])
	environment = localRunnerUpsertEnv(environment, "OPENCODE_PERMISSION", string(permission))
	return environment
}

func localRunnerOpenCodeConfig(existing, accessMode, configDir string) string {
	configuration := map[string]any{}
	if strings.TrimSpace(existing) != "" {
		_ = json.Unmarshal([]byte(existing), &configuration)
	}
	tools, _ := configuration["tools"].(map[string]any)
	if tools == nil {
		tools = map[string]any{}
	}
	tools["question"] = false
	configuration["tools"] = tools
	permission, _ := configuration["permission"].(map[string]any)
	if permission == nil {
		permission = map[string]any{}
	}
	permission["*"] = "allow"
	permission["question"] = "deny"
	if accessMode == localRunnerAccessModeFull {
		delete(permission, "external_directory")
	} else {
		permission["external_directory"] = map[string]any{
			"*": "deny",
			filepath.Join(configDir, "**"): "allow",
		}
	}
	configuration["permission"] = permission
	data, _ := json.Marshal(configuration)
	return string(data)
}

func localRunnerUpsertEnv(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if !strings.HasPrefix(item, prefix) {
			result = append(result, item)
		}
	}
	return append(result, prefix+value)
}
