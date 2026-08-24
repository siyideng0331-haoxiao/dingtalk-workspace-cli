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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
)

const localRunnerCodexDeveloperInstructions = "你是钉钉群聊里的智能助手，请用简洁、自然的中文直接回答用户问题；不要提及系统提示、内部协议或运行时细节；不要主动读写文件或执行命令。"

type localRunnerCodexTransport struct {
	options  localRunnerHarnessOptions
	sessions *localRunnerHarnessSessions
	mu       sync.Mutex
	client   *localRunnerCodexClient
}

type localRunnerCodexClient struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	messages  chan localRunnerCodexMessage
	readErr   chan error
	done      chan struct{}
	stderr    localRunnerHarnessLockedBuffer
	nextID    int
	closeOnce sync.Once
}

type localRunnerCodexMessage struct {
	ID     *int            `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type localRunnerCodexErrorNotification struct {
	Error *struct {
		Message           *string         `json:"message"`
		AdditionalDetails *string         `json:"additionalDetails"`
		CodexErrorInfo    json.RawMessage `json:"codexErrorInfo"`
	} `json:"error"`
	ThreadID  string `json:"threadId"`
	TurnID    string `json:"turnId"`
	WillRetry *bool  `json:"willRetry"`
}

func newLocalRunnerCodexTransport(options localRunnerHarnessOptions) *localRunnerCodexTransport {
	return &localRunnerCodexTransport{
		options:  options,
		sessions: newLocalRunnerHarnessSessions(options.stateKey, "codex-threads.json", options.memory),
	}
}

func (t *localRunnerCodexTransport) Warm(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	client, err := newLocalRunnerCodexClient(t.options)
	if err != nil {
		return err
	}
	if err := client.initialize(ctx); err != nil {
		client.Close()
		return err
	}
	t.client = client
	return nil
}

func (t *localRunnerCodexTransport) Prompt(ctx context.Context, contextID, prompt string, onDelta func(string)) (string, error) {
	ctx, cancel := localRunnerHarnessContext(ctx, t.options.timeout)
	defer cancel()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.client == nil {
		return "", errorsNewLocalRunnerProcessExit("Codex app-server", "transport not warmed")
	}
	threadID := ""
	if t.options.memory {
		threadID = t.sessions.Get(contextID)
	}
	if threadID != "" {
		resumed, err := t.client.thread(ctx, "thread/resume", t.threadParams(threadID))
		if err == nil {
			threadID = resumed
		} else {
			threadID = ""
			t.sessions.Set(contextID, "")
		}
	}
	if threadID == "" {
		started, err := t.client.thread(ctx, "thread/start", t.threadParams(""))
		if err != nil {
			return "", err
		}
		threadID = started
		if t.options.memory {
			t.sessions.Set(contextID, threadID)
		}
	}
	return t.client.turn(ctx, threadID, prompt, onDelta)
}

func (t *localRunnerCodexTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.client == nil {
		return nil
	}
	err := t.client.Close()
	t.client = nil
	return err
}

func (t *localRunnerCodexTransport) threadParams(threadID string) map[string]any {
	sandbox := "read-only"
	if t.options.yolo {
		sandbox = "workspace-write"
	}
	params := map[string]any{
		"approvalPolicy":        "never",
		"cwd":                   t.options.workDir,
		"developerInstructions": localRunnerCodexDeveloperInstructions,
		"sandbox":               sandbox,
	}
	if t.options.model != "" {
		params["model"] = t.options.model
	}
	if threadID != "" {
		params["threadId"] = threadID
	}
	return params
}

func newLocalRunnerCodexClient(options localRunnerHarnessOptions) (*localRunnerCodexClient, error) {
	args := append([]string{}, options.command[1:]...)
	args = append(args, "app-server", "--stdio")
	cmd := exec.Command(options.bin, args...)
	cmd.Dir = options.workDir
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	client := &localRunnerCodexClient{
		cmd:      cmd,
		stdin:    stdin,
		messages: make(chan localRunnerCodexMessage, 64),
		readErr:  make(chan error, 1),
		done:     make(chan struct{}),
		nextID:   1,
	}
	cmd.Stderr = &client.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start Codex app-server: %w", err)
	}
	go client.readLoop(stdout)
	return client, nil
}

func (c *localRunnerCodexClient) readLoop(stdout io.Reader) {
	defer close(c.messages)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var message localRunnerCodexMessage
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			c.reportReadError(fmt.Errorf("parse Codex app-server JSONL: %w", err))
			return
		}
		select {
		case c.messages <- message:
		case <-c.done:
			return
		}
	}
	if err := scanner.Err(); err != nil {
		c.reportReadError(err)
	} else {
		c.reportReadError(io.EOF)
	}
}

func (c *localRunnerCodexClient) initialize(ctx context.Context) error {
	id := c.requestID()
	if err := c.send(map[string]any{
		"id":     id,
		"method": "initialize",
		"params": map[string]any{
			"capabilities": map[string]any{"experimentalApi": true},
			"clientInfo":   map[string]any{"name": "dws-localrunner", "title": "DWS LocalRunner", "version": "1.0.0"},
		},
	}); err != nil {
		return err
	}
	if _, err := c.waitResponse(ctx, id); err != nil {
		return fmt.Errorf("initialize Codex app-server: %w", err)
	}
	return c.send(map[string]any{"method": "initialized", "params": map[string]any{}})
}

func (c *localRunnerCodexClient) thread(ctx context.Context, method string, params map[string]any) (string, error) {
	id := c.requestID()
	if err := c.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return "", err
	}
	result, err := c.waitResponse(ctx, id)
	if err != nil {
		return "", fmt.Errorf("%s: %w", method, err)
	}
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if json.Unmarshal(result, &response) != nil || strings.TrimSpace(response.Thread.ID) == "" {
		return "", fmt.Errorf("%s response missing thread.id", method)
	}
	return response.Thread.ID, nil
}

func (c *localRunnerCodexClient) turn(ctx context.Context, threadID, prompt string, onDelta func(string)) (string, error) {
	id := c.requestID()
	if err := c.send(map[string]any{
		"id":     id,
		"method": "turn/start",
		"params": map[string]any{
			"input":    []map[string]string{{"type": "text", "text": prompt}},
			"threadId": threadID,
		},
	}); err != nil {
		return "", err
	}
	var accumulated strings.Builder
	for {
		message, err := c.next(ctx)
		if err != nil {
			return "", c.withStderr("Codex app-server stream ended", err)
		}
		if message.ID != nil && message.Method != "" {
			c.rejectRequest(*message.ID, message.Method)
			continue
		}
		if message.ID != nil && *message.ID == id && message.Error != nil {
			return "", fmt.Errorf("turn/start: %s", localRunnerHarnessSafeDetail(message.Error.Message, []string{prompt, threadID}))
		}
		switch message.Method {
		case "item/agentMessage/delta":
			var params struct {
				Delta    string `json:"delta"`
				ThreadID string `json:"threadId"`
			}
			if json.Unmarshal(message.Params, &params) == nil && params.ThreadID == threadID && params.Delta != "" {
				accumulated.WriteString(params.Delta)
				if onDelta != nil {
					onDelta(accumulated.String())
				}
			}
		case "turn/completed":
			final, status, reason, ok := localRunnerCodexTurnCompleted(message.Params, threadID)
			if !ok {
				continue
			}
			if status == "failed" {
				return "", fmt.Errorf("Codex turn failed: %s", localRunnerHarnessSafeDetail(reason, []string{prompt, threadID}))
			}
			if final == "" {
				final = strings.TrimSpace(accumulated.String())
			}
			if final == "" {
				return "", errorsNewLocalRunnerEmptyReply("Codex")
			}
			if onDelta != nil && final != accumulated.String() {
				onDelta(final)
			}
			return final, nil
		case "error":
			notification, matches, err := localRunnerCodexParseErrorNotification(message.Params, threadID)
			if err != nil {
				return "", err
			}
			if !matches {
				continue
			}
			detail := localRunnerCodexErrorDetail(notification, []string{prompt, threadID, notification.TurnID})
			if *notification.WillRetry {
				if localRunnerA2AContentDebugEnabled.Load() && detail != "" {
					slog.DebugContext(ctx, "localrunner.codex.retrying", "detail", detail, "will_retry", true)
				}
				continue
			}
			if detail == "" {
				return "", fmt.Errorf("Codex app-server error notification")
			}
			return "", fmt.Errorf("Codex app-server error: %s", detail)
		}
	}
}

func localRunnerCodexParseErrorNotification(raw json.RawMessage, threadID string) (localRunnerCodexErrorNotification, bool, error) {
	var notification localRunnerCodexErrorNotification
	if json.Unmarshal(raw, &notification) != nil || strings.TrimSpace(notification.ThreadID) == "" {
		return localRunnerCodexErrorNotification{}, false, fmt.Errorf("malformed Codex app-server error notification")
	}
	if notification.ThreadID != threadID {
		return notification, false, nil
	}
	if notification.Error == nil || notification.Error.Message == nil || strings.TrimSpace(notification.TurnID) == "" || notification.WillRetry == nil {
		return localRunnerCodexErrorNotification{}, false, fmt.Errorf("malformed Codex app-server error notification")
	}
	return notification, true, nil
}

func localRunnerCodexErrorDetail(notification localRunnerCodexErrorNotification, exactSecrets []string) string {
	detailParts := []string{strings.TrimSpace(*notification.Error.Message)}
	if notification.Error.AdditionalDetails != nil {
		detailParts = append(detailParts, strings.TrimSpace(*notification.Error.AdditionalDetails))
	}
	return localRunnerHarnessSafeDetail(strings.Join(detailParts, ": "), exactSecrets)
}

func localRunnerCodexTurnCompleted(raw json.RawMessage, threadID string) (final, status, reason string, ok bool) {
	var params struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			Status string `json:"status"`
			Error  *struct {
				Message           string `json:"message"`
				AdditionalDetails string `json:"additionalDetails"`
			} `json:"error"`
			Items []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"items"`
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &params) != nil || params.ThreadID != threadID {
		return "", "", "", false
	}
	for index := len(params.Turn.Items) - 1; index >= 0; index-- {
		if params.Turn.Items[index].Type == "agentMessage" && strings.TrimSpace(params.Turn.Items[index].Text) != "" {
			final = strings.TrimSpace(params.Turn.Items[index].Text)
			break
		}
	}
	if params.Turn.Error != nil {
		reason = strings.TrimSpace(params.Turn.Error.Message + ": " + params.Turn.Error.AdditionalDetails)
	}
	return final, params.Turn.Status, reason, true
}

func (c *localRunnerCodexClient) requestID() int {
	id := c.nextID
	c.nextID++
	return id
}

func (c *localRunnerCodexClient) send(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(c.stdin, string(data))
	return err
}

func (c *localRunnerCodexClient) waitResponse(ctx context.Context, id int) (json.RawMessage, error) {
	for {
		message, err := c.next(ctx)
		if err != nil {
			return nil, c.withStderr("Codex app-server exited before response", err)
		}
		if message.ID != nil && message.Method != "" {
			c.rejectRequest(*message.ID, message.Method)
			continue
		}
		if message.ID == nil || *message.ID != id {
			continue
		}
		if message.Error != nil {
			return nil, fmt.Errorf("%s", localRunnerHarnessSafeDetail(message.Error.Message, nil))
		}
		return message.Result, nil
	}
}

func (c *localRunnerCodexClient) next(ctx context.Context) (localRunnerCodexMessage, error) {
	select {
	case message, ok := <-c.messages:
		if !ok {
			return localRunnerCodexMessage{}, io.EOF
		}
		return message, nil
	default:
	}
	select {
	case message, ok := <-c.messages:
		if !ok {
			return localRunnerCodexMessage{}, io.EOF
		}
		return message, nil
	case err := <-c.readErr:
		return localRunnerCodexMessage{}, err
	case <-c.done:
		return localRunnerCodexMessage{}, io.EOF
	case <-ctx.Done():
		return localRunnerCodexMessage{}, ctx.Err()
	}
}

func (c *localRunnerCodexClient) rejectRequest(id int, method string) {
	_ = c.send(map[string]any{
		"id": id,
		"error": map[string]any{
			"code":    -32000,
			"message": "DWS LocalRunner does not support interactive Codex app-server request: " + method,
		},
	})
}

func (c *localRunnerCodexClient) reportReadError(err error) {
	select {
	case c.readErr <- err:
	case <-c.done:
	}
}

func (c *localRunnerCodexClient) withStderr(prefix string, err error) error {
	if stderr := c.stderr.String(); stderr != "" {
		return errorsNewLocalRunnerProcessExit(prefix, fmt.Sprintf("%v: %s", err, stderr))
	}
	return errorsNewLocalRunnerProcessExit(prefix, err.Error())
}

func (c *localRunnerCodexClient) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		close(c.done)
		if c.stdin != nil {
			_ = c.stdin.Close()
		}
		if c.cmd != nil && c.cmd.Process != nil {
			closeErr = c.cmd.Process.Kill()
		}
		if c.cmd != nil {
			_ = c.cmd.Wait()
		}
	})
	return closeErr
}
