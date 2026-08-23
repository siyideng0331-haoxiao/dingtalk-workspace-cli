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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalRunnerDedicatedHarnessProcessesStayResidentAndContextsStayIsolated(t *testing.T) {
	for _, harness := range []string{"qoder", "codex", "opencode", "claudecode"} {
		t.Run(harness, func(t *testing.T) {
			stubDir := t.TempDir()
			logPath := filepath.Join(stubDir, "lifecycle.log")
			scriptPath := filepath.Join(stubDir, harness+".py")
			if err := os.WriteFile(scriptPath, []byte(localRunnerHarnessLifecycleStub(harness)), 0o600); err != nil {
				t.Fatal(err)
			}
			binary := map[string]string{"qoder": "qodercli", "codex": "codex", "opencode": "opencode", "claudecode": "claude"}[harness]
			binaryPath := filepath.Join(stubDir, binary)
			if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\nexec python3 \"$DWS_LOCALRUNNER_HARNESS_STUB\" \"$@\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DWS_CONFIG_DIR", t.TempDir())
			t.Setenv("DWS_LOCALRUNNER_HARNESS_STUB", scriptPath)
			t.Setenv("DWS_LOCALRUNNER_HARNESS_LOG", logPath)
			t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			backend, err := startLocalRunnerHarnessBackend(context.Background(), harness, localRunnerLocalAgentOptions{
				WorkDir: t.TempDir(), Memory: true, Timeout: 5 * time.Second,
			}, "localrunner-lifecycle-"+harness)
			if err != nil {
				t.Fatalf("prewarm %s: %v", harness, err)
			}
			for _, turn := range []struct {
				contextID string
				prompt string
			}{
				{contextID: "context-a", prompt: "first"},
				{contextID: "context-a", prompt: "second"},
				{contextID: "context-b", prompt: "third"},
			} {
				reply, err := backend.Stream(context.Background(), turn.contextID, turn.prompt, func(string) {})
				if err != nil || !strings.Contains(reply, turn.prompt) {
					_ = backend.Close()
					t.Fatalf("%s prompt %q reply=%q err=%v", harness, turn.prompt, reply, err)
				}
			}
			if err := backend.Close(); err != nil {
				t.Fatalf("close %s: %v", harness, err)
			}
			raw, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Fields(string(raw))
			starts := localRunnerHarnessLogValues(lines, "START")
			turns := localRunnerHarnessLogValues(lines, "TURN")
			wantStarts := 1
			if harness == "claudecode" {
				wantStarts = 2
			}
			if len(starts) != wantStarts {
				t.Fatalf("%s process starts=%q want=%d log=%s", harness, starts, wantStarts, raw)
			}
			if len(turns) != 3 || turns[0] != turns[1] || turns[0] == turns[2] {
				t.Fatalf("%s native context routing=%q log=%s", harness, turns, raw)
			}
		})
	}
}

func TestLocalRunnerClaudeContextProcessPoolIsBoundedAndResumable(t *testing.T) {
	stubDir := t.TempDir()
	logPath := filepath.Join(stubDir, "lifecycle.log")
	scriptPath := filepath.Join(stubDir, "claude.py")
	if err := os.WriteFile(scriptPath, []byte(localRunnerHarnessLifecycleStub("claudecode")), 0o600); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(stubDir, "claude")
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\nexec python3 \"$DWS_LOCALRUNNER_HARNESS_STUB\" \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DWS_CONFIG_DIR", t.TempDir())
	t.Setenv("DWS_LOCALRUNNER_HARNESS_STUB", scriptPath)
	t.Setenv("DWS_LOCALRUNNER_HARNESS_LOG", logPath)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	backend, err := startLocalRunnerHarnessBackend(context.Background(), "claudecode", localRunnerLocalAgentOptions{
		WorkDir: t.TempDir(), Memory: true, Timeout: 5 * time.Second,
	}, "localrunner-claude-pool")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	for index := 0; index < localRunnerClaudeProcessPoolLimit+2; index++ {
		contextID := fmt.Sprintf("context-%d", index)
		if _, err := backend.Prompt(context.Background(), contextID, contextID); err != nil {
			t.Fatal(err)
		}
	}
	transport, ok := backend.transport.(*localRunnerClaudeTransport)
	if !ok {
		t.Fatalf("Claude transport = %T", backend.transport)
	}
	transport.mu.Lock()
	poolSize := len(transport.processes)
	transport.mu.Unlock()
	if poolSize != localRunnerClaudeProcessPoolLimit {
		t.Fatalf("Claude pool size=%d want=%d", poolSize, localRunnerClaudeProcessPoolLimit)
	}
	if reply, err := backend.Prompt(context.Background(), "context-0", "resumed"); err != nil || !strings.Contains(reply, "resumed") {
		t.Fatalf("resume evicted context reply=%q err=%v", reply, err)
	}
}

func localRunnerHarnessLogValues(fields []string, label string) []string {
	var values []string
	for index := 0; index+1 < len(fields); index++ {
		if fields[index] == label {
			values = append(values, fields[index+1])
			index++
		}
	}
	return values
}

func localRunnerHarnessLifecycleStub(harness string) string {
	switch harness {
	case "qoder":
		return `import json, os, sys
log = os.environ["DWS_LOCALRUNNER_HARNESS_LOG"]
with open(log, "a") as f: f.write("START qoder\n")
for raw in sys.stdin:
    msg = json.loads(raw)
    if msg.get("type") == "control_request":
        print(json.dumps({"type":"control_response","response":{"subtype":"success","request_id":msg["request_id"]}}), flush=True)
    elif msg.get("type") == "user":
        sid = msg.get("session_id", "")
        prompt = msg.get("message", {}).get("content", "")
        with open(log, "a") as f: f.write("TURN " + sid + "\n")
        print(json.dumps({"type":"result","subtype":"success","result":"reply " + prompt}), flush=True)
`
	case "codex":
		return `import json, os, sys
log = os.environ["DWS_LOCALRUNNER_HARNESS_LOG"]
with open(log, "a") as f: f.write("START codex\n")
counter = 0
for raw in sys.stdin:
    msg = json.loads(raw)
    method = msg.get("method", "")
    if method == "initialize":
        print(json.dumps({"id":msg["id"],"result":{}}), flush=True)
    elif method == "thread/start":
        counter += 1
        print(json.dumps({"id":msg["id"],"result":{"thread":{"id":"thread-" + str(counter)}}}), flush=True)
    elif method == "thread/resume":
        tid = msg.get("params", {}).get("threadId", "")
        print(json.dumps({"id":msg["id"],"result":{"thread":{"id":tid}}}), flush=True)
    elif method == "turn/start":
        tid = msg["params"]["threadId"]
        prompt = msg["params"]["input"][0]["text"]
        with open(log, "a") as f: f.write("TURN " + tid + "\n")
        print(json.dumps({"method":"turn/completed","params":{"threadId":tid,"turn":{"status":"completed","items":[{"type":"agentMessage","text":"reply " + prompt}]}}}), flush=True)
`
	case "opencode":
		return `import json, os, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
log = os.environ["DWS_LOCALRUNNER_HARNESS_LOG"]
port = int(sys.argv[sys.argv.index("--port") + 1])
with open(log, "a") as f: f.write("START opencode\n")
counter = [0]
messages = {}
class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def send_json(self, value):
        raw = json.dumps(value).encode()
        self.send_response(200); self.send_header("Content-Type", "application/json"); self.send_header("Content-Length", str(len(raw))); self.end_headers(); self.wfile.write(raw)
    def do_GET(self):
        if self.path == "/global/health": self.send_json({"healthy":True})
        elif self.path == "/session/status": self.send_json({})
        elif self.path.startswith("/session/") and "/message" in self.path:
            sid = self.path.split("/")[2]; self.send_json(messages.get(sid, []))
        else: self.send_response(404); self.end_headers()
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0")); body = json.loads(self.rfile.read(length) or b"{}")
        if self.path == "/session":
            counter[0] += 1; self.send_json({"id":"session-" + str(counter[0])})
        elif self.path.startswith("/session/") and self.path.endswith("/message"):
            sid = self.path.split("/")[2]; prompt = body["parts"][0]["text"]
            with open(log, "a") as f: f.write("TURN " + sid + "\n")
            self.send_json({"parts":[{"type":"text","text":"reply " + prompt}]})
        elif self.path.startswith("/session/") and self.path.endswith("/prompt_async"):
            sid = self.path.split("/")[2]; prompt = body["parts"][0]["text"]; message_id = body["messageID"]
            with open(log, "a") as f: f.write("TURN " + sid + "\n")
            messages[sid] = [{"info":{"role":"assistant","parentID":message_id,"time":{"completed":1}},"parts":[{"type":"text","text":"reply " + prompt}]}]
            self.send_json({})
        elif self.path.startswith("/session/") and self.path.endswith("/abort"):
            self.send_json({})
        else: self.send_response(404); self.end_headers()
HTTPServer(("127.0.0.1", port), Handler).serve_forever()
`
	case "claudecode":
		return `import json, os, sys
log = os.environ["DWS_LOCALRUNNER_HARNESS_LOG"]
flag = "--resume" if "--resume" in sys.argv else "--session-id"
sid = sys.argv[sys.argv.index(flag) + 1]
with open(log, "a") as f: f.write("START " + sid + "\n")
for raw in sys.stdin:
    msg = json.loads(raw); prompt = msg["message"]["content"][0]["text"]
    with open(log, "a") as f: f.write("TURN " + sid + "\n")
    print(json.dumps({"type":"result","subtype":"success","result":"reply " + prompt}), flush=True)
`
	default:
		return ""
	}
}
