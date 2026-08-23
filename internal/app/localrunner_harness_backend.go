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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/pkg/config"
	"github.com/google/uuid"
)

var errLocalRunnerHarnessUnsupported = errors.New("local runner harness transport is unsupported")

type localRunnerHarnessOptions struct {
	harness string
	bin string
	command []string
	workDir string
	model string
	stateKey string
	memory bool
	yolo bool
	timeout time.Duration
}

type localRunnerHarnessTransport interface {
	Warm(context.Context) error
	Prompt(context.Context, string, string, func(string)) (string, error)
	Close() error
}

type localRunnerHarnessBackend struct {
	transport localRunnerHarnessTransport
	closeOnce sync.Once
	closeErr error
}

var localRunnerHarnessTransportFactory = newLocalRunnerHarnessTransport

func isLocalRunnerDedicatedHarness(harness string) bool {
	switch strings.ToLower(strings.TrimSpace(harness)) {
	case "qoder", "codex", "opencode", "claudecode":
		return true
	default:
		return false
	}
}

func startLocalRunnerHarnessBackend(ctx context.Context, harness string, options localRunnerLocalAgentOptions, stateKey string) (*localRunnerHarnessBackend, error) {
	if ctx == nil || options.Timeout < 0 {
		return nil, errors.New("invalid local runner harness configuration")
	}
	harness = strings.ToLower(strings.TrimSpace(harness))
	command, bin, err := localRunnerHarnessCommand(harness, options.AgentCommand)
	if err != nil {
		return nil, err
	}
	transport, err := localRunnerHarnessTransportFactory(localRunnerHarnessOptions{
		harness: harness,
		bin: bin,
		command: command,
		workDir: localRunnerHarnessWorkDir(options.WorkDir),
		model: strings.TrimSpace(options.Model),
		stateKey: strings.TrimSpace(stateKey),
		memory: options.Memory,
		yolo: options.Yolo,
		timeout: options.Timeout,
	})
	if err != nil {
		return nil, err
	}
	backend := &localRunnerHarnessBackend{transport: transport}
	if err := transport.Warm(ctx); err != nil {
		_ = backend.Close()
		return nil, fmt.Errorf("prewarm %s transport: %w", harness, err)
	}
	return backend, nil
}

func newLocalRunnerHarnessTransport(options localRunnerHarnessOptions) (localRunnerHarnessTransport, error) {
	switch options.harness {
	case "qoder":
		return newLocalRunnerQoderTransport(options), nil
	case "codex":
		return newLocalRunnerCodexTransport(options), nil
	case "opencode":
		return newLocalRunnerOpenCodeTransport(options), nil
	case "claudecode":
		return newLocalRunnerClaudeTransport(options), nil
	default:
		return nil, errLocalRunnerHarnessUnsupported
	}
}

func (b *localRunnerHarnessBackend) Prompt(ctx context.Context, sessionKey, prompt string) (string, error) {
	if b == nil || b.transport == nil || strings.TrimSpace(sessionKey) == "" || strings.TrimSpace(prompt) == "" {
		return "", errors.New("invalid local runner harness request")
	}
	return b.transport.Prompt(ctx, sessionKey, prompt, nil)
}

func (b *localRunnerHarnessBackend) Stream(ctx context.Context, sessionKey, prompt string, onDelta func(string)) (string, error) {
	if b == nil || b.transport == nil || strings.TrimSpace(sessionKey) == "" || strings.TrimSpace(prompt) == "" {
		return "", errors.New("invalid local runner harness request")
	}
	return b.transport.Prompt(ctx, sessionKey, prompt, onDelta)
}

func (b *localRunnerHarnessBackend) Close() error {
	if b == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		if b.transport != nil {
			b.closeErr = b.transport.Close()
		}
	})
	return b.closeErr
}

func localRunnerHarnessCommand(harness, agentCommand string) ([]string, string, error) {
	if command := strings.Fields(strings.TrimSpace(agentCommand)); len(command) > 0 && harness != "codex" {
		bin, err := exec.LookPath(command[0])
		if err != nil {
			return nil, "", err
		}
		command[0] = bin
		return command, bin, nil
	}
	binary := map[string]string{
		"qoder": "qodercli",
		"codex": "codex",
		"opencode": "opencode",
		"claudecode": "claude",
	}[harness]
	if binary == "" {
		return nil, "", errLocalRunnerHarnessUnsupported
	}
	bin, err := exec.LookPath(binary)
	if err != nil && harness == "qoder" {
		for _, pattern := range []string{
			"/Applications/Qoder.app/Contents/Resources/app/resources/bin/*/qodercli",
			"/Applications/QoderWork.app/Contents/Resources/bin/qodercli",
		} {
			matches, _ := filepath.Glob(pattern)
			if len(matches) > 0 {
				bin = matches[0]
				err = nil
				break
			}
		}
	}
	if err != nil {
		return nil, "", fmt.Errorf("%s is not installed: %w", binary, err)
	}
	return []string{bin}, bin, nil
}

func localRunnerHarnessWorkDir(raw string) string {
	if strings.TrimSpace(raw) != "" {
		if abs, err := filepath.Abs(raw); err == nil {
			return abs
		}
		return raw
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

func localRunnerHarnessContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return context.WithCancel(ctx)
}

type localRunnerHarnessSessions struct {
	mu sync.Mutex
	values map[string]string
	path string
}

func newLocalRunnerHarnessSessions(stateKey, filename string, memory bool) *localRunnerHarnessSessions {
	path := ""
	if memory && strings.TrimSpace(stateKey) != "" {
		path = filepath.Join(config.DefaultConfigDir(), "connect", localRunnerHarnessSafePath(stateKey), filename)
	}
	return &localRunnerHarnessSessions{values: localRunnerHarnessLoadSessions(path), path: path}
}

func (s *localRunnerHarnessSessions) Get(key string) string {
	if s == nil {
		return ""
	}
	key = localRunnerHarnessSessionKey(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key]
}

func (s *localRunnerHarnessSessions) GetOrCreate(key string) string {
	if s == nil {
		return uuid.NewString()
	}
	key = localRunnerHarnessSessionKey(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if value := s.values[key]; value != "" {
		return value
	}
	value := uuid.NewString()
	s.values[key] = value
	s.persistLocked()
	return value
}

func (s *localRunnerHarnessSessions) Set(key, value string) {
	if s == nil {
		return
	}
	key = localRunnerHarnessSessionKey(key)
	s.mu.Lock()
	if strings.TrimSpace(value) == "" {
		delete(s.values, key)
	} else {
		s.values[key] = value
	}
	s.persistLocked()
	s.mu.Unlock()
}

func (s *localRunnerHarnessSessions) persistLocked() {
	if s.path == "" {
		return
	}
	dir := filepath.Dir(s.path)
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	temporary, err := os.CreateTemp(dir, ".sessions-*")
	if err != nil {
		return
	}
	temporaryPath := temporary.Name()
	if temporary.Chmod(0o600) != nil {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
		return
	}
	if json.NewEncoder(temporary).Encode(s.values) != nil {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
		return
	}
	if temporary.Close() != nil {
		_ = os.Remove(temporaryPath)
		return
	}
	if os.Rename(temporaryPath, s.path) != nil {
		_ = os.Remove(temporaryPath)
	}
}

func localRunnerHarnessLoadSessions(path string) map[string]string {
	values := make(map[string]string)
	if path == "" {
		return values
	}
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &values)
	}
	return values
}

func localRunnerHarnessSessionKey(value string) string {
	if strings.TrimSpace(value) == "" {
		return "_default"
	}
	return value
}

func localRunnerHarnessSafePath(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, value)
}
