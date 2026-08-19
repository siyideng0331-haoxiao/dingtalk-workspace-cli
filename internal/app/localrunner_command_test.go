package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

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
	const wantBase = "https://api.dingtalk.com"

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

type recordingLocalRunnerRuntime struct {
	lastCall      string
	exposeOptions localRunnerExposeOptions
}

func (r *recordingLocalRunnerRuntime) Expose(_ context.Context, options localRunnerExposeOptions) (*localrunner.CreatedRunner, error) {
	r.lastCall = "expose"
	r.exposeOptions = options
	return &localrunner.CreatedRunner{RunnerID: "runner-1", EndpointID: "endpoint-1", AgentCardURL: "https://api.example.test/card", Status: localrunner.RunnerStatusActive}, nil
}

func (r *recordingLocalRunnerRuntime) Status(context.Context, string) (*localRunnerStatusResult, error) {
	r.lastCall = "status"
	return &localRunnerStatusResult{
		Runner: &localrunner.RunnerStatusData{RunnerID: "runner-1", EndpointID: "endpoint-1", LocalAgentID: "agent-1", DisplayName: "Local agent", Status: localrunner.RunnerStatusActive, AgentCardURL: "https://api.example.test/card", AgentCardSHA256: "abc123"},
		Connection: &localrunner.ConnectionStatusData{RunnerID: "runner-1", EndpointID: "endpoint-1"},
	}, nil
}

func (r *recordingLocalRunnerRuntime) Revoke(context.Context, string) (*localrunner.RevokeRunnerData, error) {
	r.lastCall = "revoke"
	return &localrunner.RevokeRunnerData{RunnerID: "runner-1", EndpointID: "endpoint-1", Status: localrunner.RunnerStatusRevoked}, nil
}

func (r *recordingLocalRunnerRuntime) Connect(context.Context, localRunnerConnectOptions) (*localrunner.ConnectionStateSnapshot, error) {
	r.lastCall = "connect"
	return &localrunner.ConnectionStateSnapshot{Identity: testIdentityForCommand(), State: localrunner.ConnectionStateReady, ConnectionID: "connection-1"}, nil
}

func testIdentityForCommand() localrunner.RunnerEndpointIdentity {
	return localrunner.RunnerEndpointIdentity{TenantID: "tenant", OperatorUserID: "operator", RunnerID: "runner-1", EndpointID: "endpoint-1"}
}
