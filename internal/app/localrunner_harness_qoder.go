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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	dwslogging "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/logging"
	"github.com/google/uuid"
)

const localRunnerQoderInitializeTimeout = 60 * time.Second

type localRunnerQoderTransport struct {
	options localRunnerHarnessOptions
	sessions *localRunnerHarnessSessions
	mu sync.Mutex
	cmd *exec.Cmd
	stdin io.WriteCloser
	lines chan string
	done chan error
	stderr localRunnerHarnessLockedBuffer
}

func newLocalRunnerQoderTransport(options localRunnerHarnessOptions) *localRunnerQoderTransport {
	return &localRunnerQoderTransport{
		options: options,
		sessions: newLocalRunnerHarnessSessions(options.stateKey, "sessions.json", options.memory),
	}
}

func (t *localRunnerQoderTransport) Warm(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.startLocked(ctx)
}

func (t *localRunnerQoderTransport) Prompt(ctx context.Context, contextID, prompt string, onDelta func(string)) (string, error) {
	ctx, cancel := localRunnerHarnessContext(ctx, t.options.timeout)
	defer cancel()
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.startLocked(ctx); err != nil {
		return "", err
	}
	sessionID := uuid.NewString()
	if t.options.memory {
		sessionID = t.sessions.GetOrCreate(contextID)
	}
	if err := t.writeLocked(map[string]any{
		"type": "user",
		"message": map[string]any{"role": "user", "content": prompt},
		"parent_tool_use_id": nil,
		"session_id": sessionID,
	}); err != nil {
		_ = t.closeLocked()
		return "", fmt.Errorf("write Qoder request: %w", err)
	}
	latest := ""
	for {
		line, err := t.readLocked(ctx)
		if err != nil {
			_ = t.closeLocked()
			return "", err
		}
		if t.handleControlRequestLocked(line) {
			continue
		}
		snapshot, final, failure, done := localRunnerParseQoderLine(line)
		if snapshot != "" && snapshot != latest {
			latest = snapshot
			if onDelta != nil {
				onDelta(snapshot)
			}
		}
		if !done {
			continue
		}
		if failure != "" {
			return "", fmt.Errorf("Qoder request failed: %s", dwslogging.SanitizeFreeText(failure, []string{prompt, contextID, sessionID}, 300))
		}
		if strings.TrimSpace(final) == "" {
			final = latest
		}
		if strings.TrimSpace(final) == "" {
			return "", errorsNewLocalRunnerEmptyReply("Qoder")
		}
		if onDelta != nil && final != latest {
			onDelta(final)
		}
		return final, nil
	}
}

func (t *localRunnerQoderTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closeLocked()
}

func (t *localRunnerQoderTransport) startLocked(ctx context.Context) error {
	if t.cmd != nil {
		select {
		case err := <-t.done:
			t.clearLocked()
			if err == nil {
				return errorsNewLocalRunnerProcessExit("Qoder", "")
			}
			return errorsNewLocalRunnerProcessExit("Qoder", err.Error())
		default:
			return nil
		}
	}
	args := append([]string{}, t.options.command[1:]...)
	args = append(args, "--print", "--output-format", "stream-json", "--input-format", "stream-json")
	if t.options.yolo {
		args = append(args, "--permission-mode", "bypass_permissions", "--dangerously-skip-permissions")
	} else {
		args = append(args, "--system-prompt", "", "--setting-sources", "", "--tools", "")
	}
	if t.options.model != "" {
		args = append(args, "--model", t.options.model)
	}
	cmd := exec.Command(t.options.bin, args...)
	cmd.Dir = t.options.workDir
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	t.stderr.Reset()
	cmd.Stderr = &t.stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start Qoder stream-json: %w", err)
	}
	t.cmd = cmd
	t.stdin = stdin
	t.lines = make(chan string, 1024)
	t.done = make(chan error, 1)
	go localRunnerScanJSONLines(stdout, t.lines)
	go func() { t.done <- cmd.Wait() }()
	if err := t.initializeLocked(ctx); err != nil {
		_ = t.closeLocked()
		return err
	}
	return nil
}

func (t *localRunnerQoderTransport) initializeLocked(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, localRunnerQoderInitializeTimeout)
	defer cancel()
	requestID := "dws_localrunner_init_" + uuid.NewString()
	if err := t.writeLocked(map[string]any{
		"type": "control_request",
		"request_id": requestID,
		"request": map[string]any{
			"type": "initialize",
			"subtype": "initialize",
			"modelPolicyProvider": false,
			"initializeTimeoutMs": int(localRunnerQoderInitializeTimeout / time.Millisecond),
		},
	}); err != nil {
		return err
	}
	for {
		line, err := t.readLocked(ctx)
		if err != nil {
			return fmt.Errorf("initialize Qoder stream-json: %w", err)
		}
		if t.handleControlRequestLocked(line) {
			continue
		}
		matched, responseErr := localRunnerQoderControlResponse(line, requestID)
		if responseErr != nil {
			return responseErr
		}
		if matched {
			return nil
		}
	}
}

func (t *localRunnerQoderTransport) writeLocked(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = t.stdin.Write(append(data, '\n'))
	return err
}

func (t *localRunnerQoderTransport) readLocked(ctx context.Context) (string, error) {
	for {
		select {
		case line, ok := <-t.lines:
			if !ok {
				return "", errorsNewLocalRunnerProcessExit("Qoder", t.stderr.String())
			}
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "{") {
				return line, nil
			}
		case err := <-t.done:
			detail := t.stderr.String()
			if detail == "" && err != nil {
				detail = err.Error()
			}
			t.clearLocked()
			return "", errorsNewLocalRunnerProcessExit("Qoder", detail)
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func (t *localRunnerQoderTransport) handleControlRequestLocked(line string) bool {
	var event struct {
		Type string `json:"type"`
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal([]byte(line), &event) != nil || event.Type != "control_request" || event.RequestID == "" {
		return false
	}
	_ = t.writeLocked(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype": "error",
			"request_id": event.RequestID,
			"error": "DWS LocalRunner does not provide host callbacks",
			"code": "unsupported_control_request",
		},
	})
	return true
}

func (t *localRunnerQoderTransport) closeLocked() error {
	if t.stdin != nil {
		_ = t.stdin.Close()
	}
	var closeErr error
	if t.cmd != nil && t.cmd.Process != nil {
		closeErr = t.cmd.Process.Kill()
	}
	if t.done != nil {
		select {
		case <-t.done:
		case <-time.After(2 * time.Second):
		}
	}
	t.clearLocked()
	return closeErr
}

func (t *localRunnerQoderTransport) clearLocked() {
	t.cmd = nil
	t.stdin = nil
	t.lines = nil
	t.done = nil
}

func localRunnerQoderControlResponse(line, requestID string) (bool, error) {
	var event struct {
		Type string `json:"type"`
		Response struct {
			Subtype string `json:"subtype"`
			RequestID string `json:"request_id"`
			Error string `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(line), &event) != nil || event.Type != "control_response" || event.Response.RequestID != requestID {
		return false, nil
	}
	if event.Response.Subtype == "error" {
		return true, fmt.Errorf("Qoder initialize rejected: %s", localRunnerHarnessSafeDetail(event.Response.Error, nil))
	}
	return true, nil
}

func localRunnerParseQoderLine(line string) (snapshot, final, failure string, done bool) {
	var event struct {
		Type string `json:"type"`
		Subtype string `json:"subtype"`
		Error string `json:"error"`
		Result string `json:"result"`
		Message struct {
			Role string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(line), &event) != nil {
		return "", "", "", false
	}
	text := func() string {
		var result strings.Builder
		for _, content := range event.Message.Content {
			if content.Type == "text" {
				result.WriteString(content.Text)
			}
		}
		return strings.TrimSpace(result.String())
	}
	switch {
	case event.Type == "assistant" && (event.Message.Role == "" || event.Message.Role == "assistant"):
		return text(), "", "", false
	case event.Type == "result":
		if event.Subtype != "" && event.Subtype != "success" {
			return "", "", strings.TrimSpace(event.Error), true
		}
		if value := text(); value != "" {
			return "", value, "", true
		}
		return "", strings.TrimSpace(event.Result), "", true
	case event.Type == "error":
		return "", "", strings.TrimSpace(event.Error), true
	default:
		return "", "", "", false
	}
}

type localRunnerHarnessLockedBuffer struct {
	mu sync.Mutex
	buffer bytes.Buffer
}

func (b *localRunnerHarnessLockedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(value)
}

func (b *localRunnerHarnessLockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(b.buffer.String())
}

func (b *localRunnerHarnessLockedBuffer) Reset() {
	b.mu.Lock()
	b.buffer.Reset()
	b.mu.Unlock()
}

func localRunnerScanJSONLines(reader io.Reader, lines chan<- string) {
	defer close(lines)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		lines <- scanner.Text()
	}
}

func errorsNewLocalRunnerProcessExit(name, detail string) error {
	detail = localRunnerHarnessSafeDetail(detail, nil)
	if detail == "" {
		return fmt.Errorf("%s process exited", name)
	}
	return fmt.Errorf("%s process exited: %s", name, detail)
}

func localRunnerHarnessSafeDetail(detail string, exactSecrets []string) string {
	return dwslogging.SanitizeFreeText(strings.TrimSpace(detail), exactSecrets, 300)
}

func errorsNewLocalRunnerEmptyReply(name string) error {
	return fmt.Errorf("%s completed without a text reply", name)
}
