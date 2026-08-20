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

package helpers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeLocalPromptKeepsContextSessionsAndRawReply(t *testing.T) {
	var created int
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/global/health":
			_, _ = w.Write([]byte(`{"healthy":true}`))
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			created++
			_, _ = w.Write([]byte(`{"id":"ses_` + string(rune('0'+created)) + `"}`))
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/session/"):
			var body struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode prompt: %v", err)
			}
			calls = append(calls, r.URL.Path+":"+body.Parts[0].Text)
			_, _ = w.Write([]byte(`{"parts":[{"type":"text","text":"  exact final reply  "}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	local := &OpenCodeLocal{forwarder: &opencodeForwarder{
		model:    "provider/model",
		sessions: newOpencodeSessions(""),
		server:   &opencodeServer{baseURL: server.URL, httpClient: server.Client()},
	}}
	for _, turn := range []struct {
		contextID string
		prompt    string
	}{
		{contextID: "context-a", prompt: "first"},
		{contextID: "context-a", prompt: "second"},
		{contextID: "context-b", prompt: "third"},
	} {
		reply, err := local.Prompt(context.Background(), turn.contextID, turn.prompt)
		if err != nil {
			t.Fatalf("Prompt(%q): %v", turn.contextID, err)
		}
		if reply != "exact final reply" {
			t.Fatalf("Prompt(%q) reply = %q", turn.contextID, reply)
		}
	}
	if created != 2 {
		t.Fatalf("created sessions = %d, want 2", created)
	}
	if got := strings.Join(calls, ","); got != "/session/ses_1/message:first,/session/ses_1/message:second,/session/ses_2/message:third" {
		t.Fatalf("message calls = %q", got)
	}
}

func TestOpenCodeLocalPromptRedactsBackendFailure(t *testing.T) {
	const sensitive = "prompt-and-server-password-must-not-escape"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/global/health":
			_, _ = w.Write([]byte(`{"healthy":true}`))
		case "/session":
			_, _ = w.Write([]byte(`{"id":"ses_error"}`))
		default:
			http.Error(w, sensitive, http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	local := &OpenCodeLocal{forwarder: &opencodeForwarder{
		sessions: newOpencodeSessions(""),
		server:   &opencodeServer{baseURL: server.URL, httpClient: server.Client()},
	}}
	_, err := local.Prompt(context.Background(), "context-a", sensitive)
	if !errors.Is(err, ErrOpenCodeLocalPromptFailed) {
		t.Fatalf("Prompt error = %v, want ErrOpenCodeLocalPromptFailed", err)
	}
	if strings.Contains(err.Error(), sensitive) {
		t.Fatalf("Prompt error leaked sensitive content: %q", err)
	}
}

func TestStartOpenCodeLocalRejectsMissingOrNonDirectoryWorkDir(t *testing.T) {
	for name, workDir := range map[string]string{"blank": "", "relative": "."} {
		t.Run(name, func(t *testing.T) {
			if _, err := StartOpenCodeLocal(context.Background(), OpenCodeLocalOptions{WorkDir: workDir}); !errors.Is(err, ErrOpenCodeLocalInvalid) {
				t.Fatalf("workdir %q error = %v, want ErrOpenCodeLocalInvalid", workDir, err)
			}
		})
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := StartOpenCodeLocal(context.Background(), OpenCodeLocalOptions{WorkDir: missing}); !errors.Is(err, ErrOpenCodeLocalInvalid) {
		t.Fatalf("missing workdir error = %v, want ErrOpenCodeLocalInvalid", err)
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := StartOpenCodeLocal(context.Background(), OpenCodeLocalOptions{WorkDir: file}); !errors.Is(err, ErrOpenCodeLocalInvalid) {
		t.Fatalf("file workdir error = %v, want ErrOpenCodeLocalInvalid", err)
	}
}

func TestStartOpenCodeLocalEagerlyStartsInWorkDirSilentlyAndClosesOnce(t *testing.T) {
	originalLocate := openCodeLocalLocateBinary
	originalExec := opencodeExecCommand
	originalPort := opencodeFreeLocalPort
	originalHealthy := opencodeWaitHealthy
	t.Cleanup(func() {
		openCodeLocalLocateBinary = originalLocate
		opencodeExecCommand = originalExec
		opencodeFreeLocalPort = originalPort
		opencodeWaitHealthy = originalHealthy
	})
	openCodeLocalLocateBinary = func() (string, bool) { return "opencode-test", true }
	opencodeFreeLocalPort = func() (int, error) { return 32123, nil }
	opencodeWaitHealthy = func(*opencodeServer, context.Context, *opencodeHTTPClient) error { return nil }
	var process *exec.Cmd
	opencodeExecCommand = func(name string, args ...string) *exec.Cmd {
		if name != "opencode-test" || strings.Join(args, " ") != "serve --pure --hostname 127.0.0.1 --port 32123" {
			t.Fatalf("OpenCode command = %q %q", name, args)
		}
		process = exec.Command("sh", "-c", "sleep 30")
		return process
	}

	workDir := t.TempDir()
	local, err := StartOpenCodeLocal(context.Background(), OpenCodeLocalOptions{WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	if process == nil || process.Process == nil || process.Dir != workDir {
		t.Fatalf("OpenCode process = %#v", process)
	}
	if local.forwarder.server.output != io.Discard {
		t.Fatal("LocalRunner OpenCode server output is not suppressed")
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	if process.ProcessState == nil || local.forwarder.server.cmd != nil {
		t.Fatalf("OpenCode process state after close = %#v", process.ProcessState)
	}
}
