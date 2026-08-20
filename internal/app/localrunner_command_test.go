package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/localrunner"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/testseam"
)

func TestLocalRunnerCommandSkeletonDelegatesWithoutSecretOutput(t *testing.T) {
	runtime := &recordingLocalRunnerRuntime{}
	testseam.Swap(t, &localRunnerCommandRuntimeProvider, func() localRunnerCommandRuntime { return runtime })

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "expose",
			args: []string{"deap", "local-runner", "expose", "--local-agent-id", "agent-1", "--display-name", "Local agent", "--agent-card-url", "http://127.0.0.1:8080/card"},
			want: "expose",
		},
		{
			name: "status",
			args: []string{"deap", "local-runner", "status", "--runner-id", "runner-1"},
			want: "status",
		},
		{
			name: "revoke",
			args: []string{"--yes", "deap", "local-runner", "revoke", "--runner-id", "runner-1"},
			want: "revoke",
		},
		{
			name: "connect",
			args: []string{"deap", "local-runner", "connect", "--runner-id", "runner-1", "--endpoint-id", "endpoint-1", "--target-url", "http://127.0.0.1:8080/rpc", "--agent-card-sha256", "abc123"},
			want: "connect",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			root := newRootCommandWithEngine(context.Background(), nil, false, true)
			root.SetOut(&output)
			root.SetErr(&output)
			root.SetArgs(tt.args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if runtime.lastCall != tt.want {
				t.Fatalf("last call = %q", runtime.lastCall)
			}
			if strings.Contains(output.String(), "endpoint-secret") || strings.Contains(output.String(), "connection-ticket") {
				t.Fatal("command output exposed a secret")
			}
		})
	}
}

func TestMergedDEAPRootKeepsLocalRunnerAndManagePublish(t *testing.T) {
	root := NewRootCommand()
	deapCommands := 0
	for _, command := range root.Commands() {
		if command.Name() == "deap" {
			deapCommands++
		}
	}
	if deapCommands != 1 {
		t.Fatalf("top-level deap commands = %d, want 1", deapCommands)
	}
	for _, path := range [][]string{
		{"deap", "local-runner", "expose"},
		{"deap", "runtime", "start-local"},
		{"deap", "manage", "publish"},
		{"deap", "skill", "query"},
	} {
		command, remaining, err := root.Find(path)
		if err != nil || len(remaining) != 0 || command == nil {
			t.Fatalf("command %v resolution = command %v remaining %v error %v", path, command, remaining, err)
		}
	}
}

func TestLocalRunnerExposeUsesFrozenLowerCamelCaseJSON(t *testing.T) {
	runtime := &recordingLocalRunnerRuntime{}
	testseam.Swap(t, &localRunnerCommandRuntimeProvider, func() localRunnerCommandRuntime { return runtime })

	var output bytes.Buffer
	root := newRootCommandWithEngine(context.Background(), nil, false, true)
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"deap", "local-runner", "expose", "--local-agent-id", "agent-1", "--display-name", "Local agent", "--agent-card-url", "http://127.0.0.1:8080/card"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	want := "{\"runnerId\":\"runner-1\",\"endpointId\":\"endpoint-1\",\"agentCardUrl\":\"https://api.example.test/card\",\"status\":\"ACTIVE\"}\n"
	if output.String() != want {
		t.Fatalf("expose output = %q, want %q", output.String(), want)
	}
}

func TestLocalRunnerExposeHelpAndSchemaUsePublicDingTalkDefaultBase(t *testing.T) {
	const wantBase = "https://pre-deap.dingtalk.com"

	var helpOutput bytes.Buffer
	helpRoot := newRootCommandWithEngine(context.Background(), nil, false, true)
	helpRoot.SetOut(&helpOutput)
	helpRoot.SetErr(&helpOutput)
	helpRoot.SetArgs([]string{"deap", "local-runner", "expose", "--help"})
	if err := helpRoot.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(helpOutput.String(), `--openapi-base string`) || !strings.Contains(helpOutput.String(), `default "`+wantBase+`"`) {
		t.Fatalf("expose help does not publish default %q:\n%s", wantBase, helpOutput.String())
	}

	var schemaOutput bytes.Buffer
	schemaRoot := NewRootCommand()
	schemaRoot.SetOut(&schemaOutput)
	schemaRoot.SetErr(&schemaOutput)
	schemaRoot.SetArgs([]string{"schema", "--cli-path", "deap local-runner expose", "-f", "json"})
	if err := schemaRoot.Execute(); err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Parameters map[string]struct {
			Default string `json:"default"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(schemaOutput.Bytes(), &schema); err != nil {
		t.Fatal(err)
	}
	if got := schema.Parameters["openapi-base"].Default; got != wantBase {
		t.Fatalf("Schema --openapi-base default = %q, want %q", got, wantBase)
	}
}

func TestLocalRunnerExposePreservesExplicitOpenAPIBaseOverride(t *testing.T) {
	runtime := &recordingLocalRunnerRuntime{}
	testseam.Swap(t, &localRunnerCommandRuntimeProvider, func() localRunnerCommandRuntime { return runtime })

	root := newRootCommandWithEngine(context.Background(), nil, false, true)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{
		"deap", "local-runner", "expose",
		"--local-agent-id", "agent-1",
		"--display-name", "Local agent",
		"--agent-card-url", "http://127.0.0.1:8080/card",
		"--openapi-base", "https://override.example.test",
	})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := runtime.exposeOptions.OpenAPIBase; got != "https://override.example.test" {
		t.Fatalf("explicit OpenAPI base = %q", got)
	}
}

func TestLocalRunnerCommandSkeletonRequiresDeclaredIdentityFlags(t *testing.T) {
	testseam.Swap(t, &localRunnerCommandRuntimeProvider, func() localRunnerCommandRuntime { return &recordingLocalRunnerRuntime{} })
	root := newRootCommandWithEngine(context.Background(), nil, false, true)
	root.SetArgs([]string{"deap", "local-runner", "connect", "--runner-id", "runner-1"})
	if err := root.Execute(); err == nil {
		t.Fatal("connect accepted an incomplete one-to-one identity")
	}
}

func TestLocalRunnerStartLocalHelpSchemaAndExactAgentReference(t *testing.T) {
	testseam.Swap(t, &localRunnerCommandRuntimeProvider, func() localRunnerCommandRuntime { return &recordingLocalRunnerRuntime{} })

	root := newRootCommandWithEngine(context.Background(), nil, false, true)
	for _, path := range []string{
		"deap runtime start-local",
		"deap local-runner expose",
		"deap local-runner status",
		"deap local-runner revoke",
		"deap local-runner connect",
	} {
		command, remaining, err := root.Find(strings.Fields(path))
		if err != nil || command == nil || len(remaining) != 0 || command.CommandPath() != "dws "+path {
			t.Fatalf("command %q resolution = %#v remaining=%v err=%v", path, command, remaining, err)
		}
	}

	for _, args := range [][]string{
		{"deap", "runtime", "start-local"},
		{"deap", "runtime", "start-local", "test-echo", "extra"},
	} {
		commandRoot := newRootCommandWithEngine(context.Background(), nil, false, true)
		commandRoot.SetOut(&bytes.Buffer{})
		commandRoot.SetErr(&bytes.Buffer{})
		commandRoot.SetArgs(args)
		if err := commandRoot.Execute(); err == nil {
			t.Fatalf("start-local accepted args %v", args)
		}
	}

	var helpOutput bytes.Buffer
	helpRoot := newRootCommandWithEngine(context.Background(), nil, false, true)
	helpRoot.SetOut(&helpOutput)
	helpRoot.SetErr(&helpOutput)
	helpRoot.SetArgs([]string{"deap", "runtime", "start-local", "--help"})
	if err := helpRoot.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"start-local <agent-ref>",
		"test-echo",
		"loopback Agent Card URL",
		"--local-agent-id",
		"--display-name",
		"--openapi-base",
		"--max-concurrent",
		"--streaming",
	} {
		if !strings.Contains(helpOutput.String(), want) {
			t.Fatalf("start-local help missing %q:\n%s", want, helpOutput.String())
		}
	}

	var schemaOutput bytes.Buffer
	schemaRoot := NewRootCommand()
	schemaRoot.SetOut(&schemaOutput)
	schemaRoot.SetErr(&schemaOutput)
	schemaRoot.SetArgs([]string{"schema", "--cli-path", "deap runtime start-local", "-f", "json"})
	if err := schemaRoot.Execute(); err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Positionals []struct {
			Name     string `json:"name"`
			Required bool   `json:"required"`
		} `json:"positionals"`
		Parameters map[string]json.RawMessage `json:"parameters"`
	}
	if err := json.Unmarshal(schemaOutput.Bytes(), &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Positionals) != 1 || schema.Positionals[0].Name != "agent_ref" || !schema.Positionals[0].Required {
		t.Fatalf("start-local positionals = %#v", schema.Positionals)
	}
	for _, name := range []string{"local-agent-id", "display-name", "openapi-base", "max-concurrent", "streaming"} {
		if schema.Parameters[name] == nil {
			t.Fatalf("start-local Schema missing --%s", name)
		}
	}
}

func TestLocalRunnerStartLocalWritesSanitizedSummaryBeforeConnectAndDoesNotRevokeOnFailure(t *testing.T) {
	connectFailure := errors.New("connect failed")
	output := &bytes.Buffer{}
	closed := false
	startResult := testLocalRunnerStartResult()
	startResult.Close = func() error {
		closed = true
		return nil
	}
	runtime := &recordingLocalRunnerRuntime{
		startResult: startResult,
		connectErr:  connectFailure,
	}
	runtime.beforeConnect = func() {
		if closed {
			t.Fatal("built-in Agent closed before Connect")
		}
		want := "{\"type\":\"A2A\",\"agentCardUrl\":\"https://pre-deap.dingtalk.com/v1/a2a/local-runners/endpoint-1/.well-known/agent-card.json\",\"localRunner\":{\"runnerId\":\"runner-1\",\"endpointId\":\"endpoint-1\",\"status\":\"CONNECTING\"}}\n"
		if output.String() != want {
			t.Fatalf("summary before connect = %q, want %q", output.String(), want)
		}
	}
	testseam.Swap(t, &localRunnerCommandRuntimeProvider, func() localRunnerCommandRuntime { return runtime })

	root := newRootCommandWithEngine(context.Background(), nil, false, true)
	root.SetOut(output)
	root.SetErr(output)
	root.SetArgs([]string{
		"deap", "runtime", "start-local", "test-echo",
		"--local-agent-id", "agent-override",
		"--display-name", "Display override",
		"--openapi-base", "https://override.example.test",
		"--max-concurrent", "7",
		"--streaming=false",
	})
	err := root.Execute()
	if !errors.Is(err, connectFailure) {
		t.Fatalf("start-local error = %v", err)
	}
	if runtime.revokeCalls != 0 {
		t.Fatalf("connect failure triggered %d revoke calls", runtime.revokeCalls)
	}
	if !closed {
		t.Fatal("built-in Agent was not closed after Connect returned")
	}
	if runtime.startOptions.AgentRef != "test-echo" || runtime.startOptions.LocalAgentID != "agent-override" || runtime.startOptions.DisplayName != "Display override" || runtime.startOptions.OpenAPIBase != "https://override.example.test" || runtime.startOptions.MaxConcurrent != 7 || runtime.startOptions.Streaming {
		t.Fatalf("start-local options = %#v", runtime.startOptions)
	}
	for _, secret := range []string{"endpoint-secret", "connection-ticket", "local-authorization"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("start-local output exposed %q", secret)
		}
	}
}

func TestLocalRunnerStartLocalContextCancelStopsCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	connectStarted := make(chan struct{})
	closed := false
	startResult := testLocalRunnerStartResult()
	startResult.Close = func() error {
		closed = true
		return nil
	}
	runtime := &recordingLocalRunnerRuntime{
		startResult:    startResult,
		connectStarted: connectStarted,
		waitForCancel:  true,
	}
	testseam.Swap(t, &localRunnerCommandRuntimeProvider, func() localRunnerCommandRuntime { return runtime })

	root := newRootCommandWithEngine(ctx, nil, false, true)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"deap", "runtime", "start-local", "http://127.0.0.1:8080/card"})
	done := make(chan error, 1)
	go func() { done <- root.Execute() }()
	select {
	case <-connectStarted:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("start-local did not enter connect")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("start-local cancellation error = %v", err)
		}
		if !closed {
			t.Fatal("built-in Agent was not closed after context cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("start-local did not stop after context cancellation")
	}
}

type recordingLocalRunnerRuntime struct {
	lastCall       string
	exposeOptions  localRunnerExposeOptions
	startOptions   localRunnerStartLocalOptions
	startResult    *localRunnerStartLocalResult
	connectErr     error
	beforeConnect  func()
	connectStarted chan struct{}
	waitForCancel  bool
	revokeCalls    int
}

func (r *recordingLocalRunnerRuntime) Expose(_ context.Context, options localRunnerExposeOptions) (*localrunner.CreatedRunner, error) {
	r.lastCall = "expose"
	r.exposeOptions = options
	return &localrunner.CreatedRunner{RunnerID: "runner-1", EndpointID: "endpoint-1", AgentCardURL: "https://api.example.test/card", Status: localrunner.RunnerStatusActive}, nil
}

func (r *recordingLocalRunnerRuntime) Status(context.Context, string) (*localRunnerStatusResult, error) {
	r.lastCall = "status"
	return &localRunnerStatusResult{
		Runner: &localrunner.RunnerStatusData{RunnerID: "runner-1", EndpointID: "endpoint-1", LocalAgentID: "agent-1", DisplayName: "Local agent", Status: localrunner.RunnerStatusActive, AgentCardURL: "https://api.example.test/card", AgentCardSHA256: "sha256:" + strings.Repeat("a", 64)},
		Connection: &localrunner.ConnectionStatusData{RunnerID: "runner-1", EndpointID: "endpoint-1"},
	}, nil
}

func (r *recordingLocalRunnerRuntime) Revoke(context.Context, string) (*localrunner.RevokeRunnerData, error) {
	r.lastCall = "revoke"
	r.revokeCalls++
	return &localrunner.RevokeRunnerData{RunnerID: "runner-1", EndpointID: "endpoint-1", Status: localrunner.RunnerStatusRevoked}, nil
}

func (r *recordingLocalRunnerRuntime) StartLocal(_ context.Context, options localRunnerStartLocalOptions) (*localRunnerStartLocalResult, error) {
	r.lastCall = "start-local"
	r.startOptions = options
	if r.startResult != nil {
		return r.startResult, nil
	}
	return testLocalRunnerStartResult(), nil
}

func (r *recordingLocalRunnerRuntime) Connect(ctx context.Context, _ localRunnerConnectOptions) (*localrunner.ConnectionStateSnapshot, error) {
	r.lastCall = "connect"
	if r.beforeConnect != nil {
		r.beforeConnect()
	}
	if r.connectStarted != nil {
		close(r.connectStarted)
	}
	if r.waitForCancel {
		<-ctx.Done()
		return &localrunner.ConnectionStateSnapshot{Identity: testIdentityForCommand(), State: localrunner.ConnectionStateStopped}, nil
	}
	if r.connectErr != nil {
		return nil, r.connectErr
	}
	return &localrunner.ConnectionStateSnapshot{Identity: testIdentityForCommand(), State: localrunner.ConnectionStateReady, ConnectionID: "connection-1"}, nil
}

func testLocalRunnerStartResult() *localRunnerStartLocalResult {
	return &localRunnerStartLocalResult{
		Summary: localRunnerA2AConfiguration{
			Type:         "A2A",
			AgentCardURL: "https://pre-deap.dingtalk.com/v1/a2a/local-runners/endpoint-1/.well-known/agent-card.json",
			LocalRunner: localRunnerA2ALocalRunner{RunnerID: "runner-1", EndpointID: "endpoint-1", Status: "CONNECTING"},
		},
		ConnectOptions: localRunnerConnectOptions{
			RunnerID: "runner-1", EndpointID: "endpoint-1", TargetURL: "http://127.0.0.1:8080/rpc",
			AgentCardSHA256: "sha256:" + strings.Repeat("a", 64), MaxConcurrent: 4, Streaming: true,
		},
	}
}

func testIdentityForCommand() localrunner.RunnerEndpointIdentity {
	return localrunner.RunnerEndpointIdentity{TenantID: "tenant", OperatorUserID: "operator", RunnerID: "runner-1", EndpointID: "endpoint-1"}
}
