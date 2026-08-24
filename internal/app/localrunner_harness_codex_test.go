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
	"encoding/json"
	"strings"
	"testing"
)

func TestLocalRunnerCodexTurnContinuesAfterRetryableError(t *testing.T) {
	client := newTestLocalRunnerCodexClient(t,
		newTestLocalRunnerCodexNotification(t, "error", map[string]any{
			"error": map[string]any{
				"message":           "Responses WebSocket disconnected",
				"additionalDetails": "Connection reset by peer (os error 54)",
				"codexErrorInfo":    nil,
			},
			"threadId":  "thread-current",
			"turnId":    "turn-current",
			"willRetry": true,
		}),
		newTestLocalRunnerCodexNotification(t, "item/agentMessage/delta", map[string]any{
			"delta":    "recovered answer",
			"threadId": "thread-current",
		}),
		newTestLocalRunnerCodexNotification(t, "turn/completed", map[string]any{
			"threadId": "thread-current",
			"turn": map[string]any{
				"status": "completed",
				"items":  []map[string]any{{"type": "agentMessage", "text": "recovered answer"}},
			},
		}),
	)

	var deltas []string
	got, err := client.turn(context.Background(), "thread-current", "private prompt", func(delta string) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatalf("turn returned error: %v", err)
	}
	if got != "recovered answer" {
		t.Fatalf("turn reply = %q, want recovered answer", got)
	}
	if len(deltas) != 1 || deltas[0] != "recovered answer" {
		t.Fatalf("turn deltas = %#v, want recovered answer", deltas)
	}
}

func TestLocalRunnerCodexTurnLogsSanitizedRetryOnlyInDebug(t *testing.T) {
	const (
		prompt   = "private retry prompt"
		threadID = "thread-retry-sensitive-42"
		turnID   = "turn-retry-sensitive-42"
	)
	for _, test := range []struct {
		name  string
		debug bool
	}{
		{name: "debug", debug: true},
		{name: "info", debug: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs := captureLocalRunnerA2AMessageLogs(t, test.debug, func() {
				client := newTestLocalRunnerCodexClient(t,
					newTestLocalRunnerCodexNotification(t, "error", map[string]any{
						"error": map[string]any{
							"message": "Responses WebSocket disconnected",
							"additionalDetails": "Connection reset by peer (os error 54); password=hunter2 " +
								"token=tok-retry Authorization: Bearer bearer-retry " + prompt + " " + threadID + " " + turnID,
							"codexErrorInfo": map[string]any{"providerCredential": "structured-retry-secret"},
						},
						"threadId":  threadID,
						"turnId":    turnID,
						"willRetry": true,
					}),
					newTestLocalRunnerCodexNotification(t, "turn/completed", map[string]any{
						"threadId": threadID,
						"turn": map[string]any{
							"status": "completed",
							"items":  []map[string]any{{"type": "agentMessage", "text": "recovered answer"}},
						},
					}),
				)
				if _, err := client.turn(context.Background(), threadID, prompt, nil); err != nil {
					t.Fatalf("turn returned error: %v", err)
				}
			})

			var retryRecord map[string]any
			for _, line := range strings.Split(strings.TrimSpace(logs.raw), "\n") {
				var record map[string]any
				if json.Unmarshal([]byte(line), &record) == nil && record["msg"] == "localrunner.codex.retrying" {
					retryRecord = record
					break
				}
			}
			if !test.debug {
				if retryRecord != nil || strings.Contains(logs.raw, "Connection reset by peer") {
					t.Fatalf("non-debug run emitted retry detail: %s", logs.raw)
				}
				return
			}
			if retryRecord == nil {
				t.Fatalf("debug retry record missing: %s", logs.raw)
			}
			if retryRecord["will_retry"] != true {
				t.Fatalf("retry record = %#v, want will_retry=true", retryRecord)
			}
			detail, _ := retryRecord["detail"].(string)
			if !strings.Contains(detail, "Responses WebSocket disconnected") || !strings.Contains(detail, "Connection reset by peer") {
				t.Fatalf("retry detail = %q, want safe actionable reason", detail)
			}
			for _, secret := range []string{prompt, threadID, turnID, "hunter2", "tok-retry", "bearer-retry", "structured-retry-secret"} {
				if strings.Contains(logs.raw, secret) {
					t.Fatalf("retry log leaked %q: %s", secret, logs.raw)
				}
			}
		})
	}
}

func TestLocalRunnerCodexTurnIgnoresErrorForAnotherThread(t *testing.T) {
	client := newTestLocalRunnerCodexClient(t,
		newTestLocalRunnerCodexNotification(t, "error", map[string]any{
			"error": map[string]any{
				"message":           "another turn failed",
				"additionalDetails": nil,
				"codexErrorInfo":    nil,
			},
			"threadId":  "thread-other",
			"turnId":    "turn-other",
			"willRetry": false,
		}),
		newTestLocalRunnerCodexNotification(t, "turn/completed", map[string]any{
			"threadId": "thread-current",
			"turn": map[string]any{
				"status": "completed",
				"items":  []map[string]any{{"type": "agentMessage", "text": "current answer"}},
			},
		}),
	)

	got, err := client.turn(context.Background(), "thread-current", "private prompt", nil)
	if err != nil {
		t.Fatalf("turn returned error: %v", err)
	}
	if got != "current answer" {
		t.Fatalf("turn reply = %q, want current answer", got)
	}
}

func TestLocalRunnerCodexTurnReturnsSanitizedTerminalError(t *testing.T) {
	const (
		prompt   = "private customer prompt"
		threadID = "thread-sensitive-42"
		turnID   = "turn-sensitive-42"
	)
	client := newTestLocalRunnerCodexClient(t,
		newTestLocalRunnerCodexNotification(t, "error", map[string]any{
			"error": map[string]any{
				"message": "Responses WebSocket failed",
				"additionalDetails": "not logged in; password=hunter2 token=tok-terminal " +
					"Authorization: Bearer bearer-terminal " + prompt + " " + threadID + " " + turnID,
				"codexErrorInfo": map[string]any{"providerCredential": "structured-secret"},
			},
			"threadId":  threadID,
			"turnId":    turnID,
			"willRetry": false,
		}),
	)

	_, err := client.turn(context.Background(), threadID, prompt, nil)
	if err == nil {
		t.Fatal("turn error = nil, want terminal Codex error")
	}
	detail := err.Error()
	for _, want := range []string{"Responses WebSocket failed", "not logged in"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("turn error = %q, want safe detail %q", detail, want)
		}
	}
	for _, secret := range []string{prompt, threadID, turnID, "hunter2", "tok-terminal", "bearer-terminal", "structured-secret"} {
		if strings.Contains(detail, secret) {
			t.Fatalf("turn error leaked %q: %q", secret, detail)
		}
	}
}

func TestLocalRunnerCodexTurnFailsSafelyOnMalformedErrorNotification(t *testing.T) {
	client := newTestLocalRunnerCodexClient(t,
		newTestLocalRunnerCodexNotification(t, "error", map[string]any{
			"error":    map[string]any{"message": "private malformed detail"},
			"threadId": "thread-current",
			"turnId":   "turn-current",
		}),
	)

	_, err := client.turn(context.Background(), "thread-current", "private prompt", nil)
	if err == nil {
		t.Fatal("turn error = nil, want malformed-notification error")
	}
	if got := err.Error(); got != "malformed Codex app-server error notification" {
		t.Fatalf("turn error = %q, want static malformed-notification error", got)
	}
}

type testLocalRunnerCodexWriteCloser struct {
	bytes.Buffer
}

func (w *testLocalRunnerCodexWriteCloser) Close() error { return nil }

func newTestLocalRunnerCodexClient(t *testing.T, notifications ...localRunnerCodexMessage) *localRunnerCodexClient {
	t.Helper()
	messages := make(chan localRunnerCodexMessage, len(notifications))
	for _, notification := range notifications {
		messages <- notification
	}
	close(messages)
	return &localRunnerCodexClient{
		stdin:    &testLocalRunnerCodexWriteCloser{},
		messages: messages,
		readErr:  make(chan error, 1),
		done:     make(chan struct{}),
		nextID:   1,
	}
}

func newTestLocalRunnerCodexNotification(t *testing.T, method string, params any) localRunnerCodexMessage {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal %s params: %v", method, err)
	}
	return localRunnerCodexMessage{Method: method, Params: raw}
}
