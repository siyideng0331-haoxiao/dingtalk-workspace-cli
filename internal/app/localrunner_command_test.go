package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/helpers"
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
			args: []string{"deap", "runtime", "expose", "--local-agent-id", "agent-1", "--display-name", "Local agent", "--agent-card-url", "http://127.0.0.1:8080/card"},
			want: "expose",
		},
		{
			name: "status",
			args: []string{"deap", "runtime", "status", "--runner-id", "runner-1"},
			want: "status",
		},
		{
			name: "revoke",
			args: []string{"--yes", "deap", "runtime", "revoke", "--runner-id", "runner-1"},
			want: "revoke",
		},
		{
			name: "remove-local",
			args: []string{"--yes", "deap", "runtime", "remove-local", "--runner-id", "runner-1"},
			want: "revoke",
		},
		{
			name: "connect",
			args: []string{"deap", "runtime", "connect", "--runner-id", "runner-1", "--endpoint-id", "endpoint-1", "--target-url", "http://127.0.0.1:8080/rpc", "--agent-card-sha256", "abc123"},
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

func TestLocalRunnerCommandsAreConsolidatedUnderRuntimeWithoutOpenAPIBaseFlag(t *testing.T) {
	root := NewRootCommand()
	for _, leaf := range []string{"expose", "status", "remove-local", "revoke", "connect", "start-local"} {
		path := []string{"deap", "runtime", leaf}
		command, remaining, err := root.Find(path)
		if err != nil || command == nil || len(remaining) != 0 {
			t.Fatalf("command %v resolution = command=%v remaining=%v error=%v", path, command, remaining, err)
		}
	}
	var helpOutput bytes.Buffer
	helpRoot := NewRootCommand()
	helpRoot.SetOut(&helpOutput)
	helpRoot.SetErr(&helpOutput)
	helpRoot.SetArgs([]string{"deap", "runtime", "--help"})
	if err := helpRoot.Execute(); err != nil {
		t.Fatal(err)
	}
	availableParts := strings.SplitN(helpOutput.String(), "Available Commands:", 2)
	if len(availableParts) != 2 {
		t.Fatalf("runtime help missing available commands section:\n%s", helpOutput.String())
	}
	available := strings.SplitN(availableParts[1], "\n\nFlags:", 2)[0]
	for _, public := range []string{"remove-local", "start-local", "status"} {
		if !strings.Contains(available, "\n  "+public) {
			t.Fatalf("runtime help missing public command %q:\n%s", public, helpOutput.String())
		}
	}
	for _, hidden := range []string{"connect", "expose", "revoke"} {
		if strings.Contains(available, "\n  "+hidden) {
			t.Fatalf("runtime help publishes compatibility command %q:\n%s", hidden, helpOutput.String())
		}
	}
	if command, remaining, err := root.Find([]string{"deap", "local-runner"}); err == nil && command != nil && len(remaining) == 0 && command.CommandPath() == "dws deap local-runner" {
		t.Fatal("deprecated deap local-runner group is still executable")
	}
	for _, leaf := range []string{"expose", "start-local"} {
		command, _, err := root.Find([]string{"deap", "runtime", leaf})
		if err != nil {
			t.Fatal(err)
		}
		if command.Flags().Lookup("openapi-base") != nil {
			t.Fatalf("deap runtime %s still publishes --openapi-base", leaf)
		}
	}
}

func TestLocalRunnerRemoveLocalKeepsRevokeSchemaAlias(t *testing.T) {
	for _, test := range []struct {
		path    string
		isAlias bool
	}{
		{path: "deap runtime remove-local"},
		{path: "deap runtime revoke", isAlias: true},
	} {
		t.Run(test.path, func(t *testing.T) {
			var schemaOutput bytes.Buffer
			root := NewRootCommand()
			root.SetOut(&schemaOutput)
			root.SetErr(&schemaOutput)
			root.SetArgs([]string{"schema", "--cli-path", test.path, "-f", "json"})
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			var schema struct {
				CanonicalPath  string `json:"canonical_path"`
				CLIPath        string `json:"cli_path"`
				PrimaryCLIPath string `json:"primary_cli_path"`
				IsAlias        bool   `json:"is_alias"`
			}
			if err := json.Unmarshal(schemaOutput.Bytes(), &schema); err != nil {
				t.Fatal(err)
			}
			if schema.CanonicalPath != "deap.runtime_revoke" || schema.CLIPath != test.path || schema.PrimaryCLIPath != "deap runtime remove-local" || schema.IsAlias != test.isAlias {
				t.Fatalf("remove-local Schema for %q = %#v", test.path, schema)
			}
		})
	}
}

func TestMergedDEAPRootKeepsRuntimeAndManagePublish(t *testing.T) {
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
		{"deap", "runtime", "expose"},
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
	root.SetArgs([]string{"deap", "runtime", "expose", "--local-agent-id", "agent-1", "--display-name", "Local agent", "--agent-card-url", "http://127.0.0.1:8080/card"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	want := "{\"runnerId\":\"runner-1\",\"endpointId\":\"endpoint-1\",\"agentCardUrl\":\"https://api.example.test/card\",\"status\":\"ACTIVE\"}\n"
	if output.String() != want {
		t.Fatalf("expose output = %q, want %q", output.String(), want)
	}
}

func TestLocalRunnerExposeHelpAndSchemaDoNotExposeOpenAPIBase(t *testing.T) {
	var helpOutput bytes.Buffer
	helpRoot := newRootCommandWithEngine(context.Background(), nil, false, true)
	helpRoot.SetOut(&helpOutput)
	helpRoot.SetErr(&helpOutput)
	helpRoot.SetArgs([]string{"deap", "runtime", "expose", "--help"})
	if err := helpRoot.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(helpOutput.String(), `--openapi-base`) {
		t.Fatalf("expose help still publishes --openapi-base:\n%s", helpOutput.String())
	}

	var schemaOutput bytes.Buffer
	schemaRoot := NewRootCommand()
	schemaRoot.SetOut(&schemaOutput)
	schemaRoot.SetErr(&schemaOutput)
	schemaRoot.SetArgs([]string{"schema", "--cli-path", "deap runtime expose", "-f", "json"})
	if err := schemaRoot.Execute(); err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Parameters map[string]json.RawMessage `json:"parameters"`
	}
	if err := json.Unmarshal(schemaOutput.Bytes(), &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Parameters["openapi-base"] != nil {
		t.Fatalf("Schema still publishes --openapi-base: %s", schemaOutput.String())
	}
}

func TestLocalRunnerExposeRejectsOpenAPIBaseFlag(t *testing.T) {
	runtime := &recordingLocalRunnerRuntime{}
	testseam.Swap(t, &localRunnerCommandRuntimeProvider, func() localRunnerCommandRuntime { return runtime })

	root := newRootCommandWithEngine(context.Background(), nil, false, true)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{
		"deap", "runtime", "expose",
		"--local-agent-id", "agent-1",
		"--display-name", "Local agent",
		"--agent-card-url", "http://127.0.0.1:8080/card",
		"--openapi-base", "https://override.example.test",
	})
	if err := root.Execute(); err == nil {
		t.Fatal("expose accepted removed --openapi-base flag")
	}
}

func TestLocalRunnerCommandSkeletonRequiresDeclaredIdentityFlags(t *testing.T) {
	testseam.Swap(t, &localRunnerCommandRuntimeProvider, func() localRunnerCommandRuntime { return &recordingLocalRunnerRuntime{} })
	root := newRootCommandWithEngine(context.Background(), nil, false, true)
	root.SetArgs([]string{"deap", "runtime", "connect", "--runner-id", "runner-1"})
	if err := root.Execute(); err == nil {
		t.Fatal("connect accepted an incomplete one-to-one identity")
	}
}

func TestLocalRunnerStartLocalHelpSchemaUsesRequiredHarnessAndWorkDir(t *testing.T) {
	testseam.Swap(t, &localRunnerCommandRuntimeProvider, func() localRunnerCommandRuntime { return &recordingLocalRunnerRuntime{} })

	root := newRootCommandWithEngine(context.Background(), nil, false, true)
	for _, path := range []string{
		"deap runtime start-local",
		"deap runtime expose",
		"deap runtime status",
		"deap runtime connect",
	} {
		command, remaining, err := root.Find(strings.Fields(path))
		if err != nil || command == nil || len(remaining) != 0 || command.CommandPath() != "dws "+path {
			t.Fatalf("command %q resolution = %#v remaining=%v err=%v", path, command, remaining, err)
		}
	}
	for _, args := range [][]string{
		{"deap", "runtime", "start-local"},
		{"deap", "runtime", "start-local", "opencode", "--work-dir", t.TempDir()},
		{"deap", "runtime", "start-local", "--harness", "opencode", "--workdir", t.TempDir()},
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
		"start-local [flags]",
		"--harness",
		"--work-dir",
		"opencode",
		"--model",
		"--agent-cmd",
		"仅 custom harness 使用",
		"--runner-id",
		"仅用于本地配置丢失或迁移时的灾难恢复",
	} {
		if !strings.Contains(helpOutput.String(), want) {
			t.Fatalf("start-local help missing %q:\n%s", want, helpOutput.String())
		}
	}
	for _, want := range []string{"日常重启可直接重跑原命令", "恢复", "WSS", "HTTP/SSE"} {
		if !strings.Contains(helpOutput.String(), want) {
			t.Fatalf("start-local help missing recovery guidance %q:\n%s", want, helpOutput.String())
		}
	}
	for _, removed := range []string{"<agent-ref>", "--workdir", "--endpoint-id", "test-echo", "loopback Agent Card URL", "--local-agent-id", "--display-name", "--max-concurrent", "--streaming", "--memory", "--yolo", "--agent-timeout"} {
		if strings.Contains(helpOutput.String(), removed) {
			t.Fatalf("start-local help still publishes removed contract %q:\n%s", removed, helpOutput.String())
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
	if len(schema.Positionals) != 0 {
		t.Fatalf("start-local positionals = %#v", schema.Positionals)
	}
	for _, name := range []string{"harness", "work-dir", "model", "agent-cmd", "runner-id"} {
		if schema.Parameters[name] == nil {
			t.Fatalf("start-local Schema missing --%s", name)
		}
	}
	for _, removed := range []string{"workdir", "openapi-base", "endpoint-id", "local-agent-id", "display-name", "max-concurrent", "streaming", "memory", "yolo", "agent-timeout"} {
		if schema.Parameters[removed] != nil {
			t.Fatalf("start-local Schema still publishes --%s", removed)
		}
	}
	var harnessParameter struct {
		Required bool     `json:"required"`
		Enum     []string `json:"enum"`
	}
	if err := json.Unmarshal(schema.Parameters["harness"], &harnessParameter); err != nil {
		t.Fatal(err)
	}
	if !harnessParameter.Required || strings.Join(harnessParameter.Enum, ",") != strings.Join(helpers.LocalRunnerAgentChannels(), ",") {
		t.Fatalf("start-local harness contract = %#v", harnessParameter)
	}
	for _, harness := range helpers.LocalRunnerAgentChannels() {
		if !strings.Contains(helpOutput.String(), harness) {
			t.Fatalf("start-local help missing shared harness %q:\n%s", harness, helpOutput.String())
		}
	}
	var workDirParameter struct {
		Required bool `json:"required"`
	}
	if err := json.Unmarshal(schema.Parameters["work-dir"], &workDirParameter); err != nil {
		t.Fatal(err)
	}
	if !workDirParameter.Required {
		t.Fatalf("start-local work-dir contract = %#v", workDirParameter)
	}
	var agentCommandParameter struct {
		Required     bool   `json:"required"`
		RequiredWhen string `json:"required_when"`
	}
	if err := json.Unmarshal(schema.Parameters["agent-cmd"], &agentCommandParameter); err != nil {
		t.Fatal(err)
	}
	if agentCommandParameter.Required || agentCommandParameter.RequiredWhen != "harness=custom unless DWS_AGENT_CMD is set" {
		t.Fatalf("start-local agent-cmd contract = %#v", agentCommandParameter)
	}
	var runnerParameter struct {
		Required     bool   `json:"required"`
		RequiredWhen string `json:"required_when"`
	}
	if err := json.Unmarshal(schema.Parameters["runner-id"], &runnerParameter); err != nil {
		t.Fatal(err)
	}
	if runnerParameter.Required || runnerParameter.RequiredWhen != "" {
		t.Fatalf("start-local runner-id contract = %#v", runnerParameter)
	}
}

func TestLocalRunnerPublicSelectionDoesNotRouteToHiddenCommands(t *testing.T) {
	for _, path := range []string{"deap runtime start-local", "deap runtime status"} {
		t.Run(path, func(t *testing.T) {
			var schemaOutput bytes.Buffer
			root := NewRootCommand()
			root.SetOut(&schemaOutput)
			root.SetErr(&schemaOutput)
			root.SetArgs([]string{"schema", "--cli-path", path, "-f", "json"})
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			var schema struct {
				Description string   `json:"description"`
				UseWhen     []string `json:"use_when"`
				AvoidWhen   []string `json:"avoid_when"`
			}
			if err := json.Unmarshal(schemaOutput.Bytes(), &schema); err != nil {
				t.Fatal(err)
			}
			guidance := strings.Join(append(append([]string{schema.Description}, schema.UseWhen...), schema.AvoidWhen...), "\n")
			for _, hidden := range []string{"deap runtime expose", "使用 expose", "deap runtime connect", "使用 connect"} {
				if strings.Contains(guidance, hidden) {
					t.Fatalf("%s public guidance routes to hidden command %q:\n%s", path, hidden, guidance)
				}
			}
			for _, public := range []string{"start-local", "remove-local"} {
				if !strings.Contains(guidance, public) {
					t.Fatalf("%s public guidance missing %q:\n%s", path, public, guidance)
				}
			}
		})
	}
}

func TestLocalRunnerStartLocalOpenCodeNormalizesWorkDirAndPassesModel(t *testing.T) {
	runtime := &recordingLocalRunnerRuntime{}
	testseam.Swap(t, &localRunnerCommandRuntimeProvider, func() localRunnerCommandRuntime { return runtime })
	workDir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, workDir)
	if err != nil {
		t.Fatal(err)
	}
	root := newRootCommandWithEngine(context.Background(), nil, false, true)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"deap", "runtime", "start-local", "--harness", "opencode", "--work-dir", relative, "--model", "provider/model"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if runtime.startOptions.AgentRef != "opencode" || runtime.startOptions.WorkDir != filepath.Clean(workDir) || runtime.startOptions.Model != "provider/model" {
		t.Fatalf("start-local OpenCode options = %#v", runtime.startOptions)
	}
}

func TestLocalRunnerStartLocalUsesSharedHarnessRegistry(t *testing.T) {
	for _, harness := range helpers.LocalRunnerAgentChannels() {
		t.Run(harness, func(t *testing.T) {
			runtime := &recordingLocalRunnerRuntime{}
			testseam.Swap(t, &localRunnerCommandRuntimeProvider, func() localRunnerCommandRuntime { return runtime })
			output := &bytes.Buffer{}
			root := newRootCommandWithEngine(context.Background(), nil, false, true)
			root.SetOut(output)
			root.SetErr(output)
			args := []string{"deap", "runtime", "start-local", "--harness", harness, "--work-dir", t.TempDir()}
			if harness == "custom" {
				args = append(args, "--agent-cmd", "custom-agent --safe-flag")
			}
			root.SetArgs(args)
			if err := root.Execute(); err != nil {
				t.Fatalf("start-local harness %q error = %v", harness, err)
			}
			if runtime.startOptions.AgentRef != harness {
				t.Fatalf("start-local harness %q options = %#v", harness, runtime.startOptions)
			}
			if harness == "custom" && runtime.startOptions.AgentCommand != "custom-agent --safe-flag" {
				t.Fatalf("custom agent command was not passed in memory: %#v", runtime.startOptions)
			}
			if strings.Contains(output.String(), "custom-agent") {
				t.Fatalf("start-local output exposed custom command: %s", output.String())
			}
		})
	}

	for _, harness := range []string{"unknown", "auto", "gemini", "openclaw", "hermes", "test-echo"} {
		t.Run("reject-"+harness, func(t *testing.T) {
			runtime := &recordingLocalRunnerRuntime{}
			testseam.Swap(t, &localRunnerCommandRuntimeProvider, func() localRunnerCommandRuntime { return runtime })
			root := newRootCommandWithEngine(context.Background(), nil, false, true)
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
			root.SetArgs([]string{"deap", "runtime", "start-local", "--harness", harness, "--work-dir", t.TempDir()})
			if err := root.Execute(); err == nil {
				t.Fatalf("start-local accepted unsupported harness %q", harness)
			}
			if runtime.lastCall != "" {
				t.Fatalf("rejected harness %q reached runtime", harness)
			}
		})
	}
}

func TestLocalRunnerStartLocalCustomRequiresCommandAndRejectsCommandForOtherHarnesses(t *testing.T) {
	t.Setenv("DWS_AGENT_CMD", "")
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "custom missing command", args: []string{"deap", "runtime", "start-local", "--harness", "custom", "--work-dir", t.TempDir()}},
		{name: "command on opencode", args: []string{"deap", "runtime", "start-local", "--harness", "opencode", "--work-dir", t.TempDir(), "--agent-cmd", "custom-agent"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &recordingLocalRunnerRuntime{}
			testseam.Swap(t, &localRunnerCommandRuntimeProvider, func() localRunnerCommandRuntime { return runtime })
			root := newRootCommandWithEngine(context.Background(), nil, false, true)
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
			root.SetArgs(test.args)
			if err := root.Execute(); err == nil {
				t.Fatalf("start-local accepted invalid custom command contract: %v", test.args)
			}
			if runtime.lastCall != "" {
				t.Fatalf("invalid custom command contract reached runtime as %q", runtime.lastCall)
			}
		})
	}
}

func TestLocalRunnerStartLocalAcceptsRunnerOnlyRecoveryAndRejectsEndpointFlag(t *testing.T) {
	workDir := t.TempDir()
	runtime := &recordingLocalRunnerRuntime{}
	testseam.Swap(t, &localRunnerCommandRuntimeProvider, func() localRunnerCommandRuntime { return runtime })
	root := newRootCommandWithEngine(context.Background(), nil, false, true)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"deap", "runtime", "start-local", "--harness", "opencode", "--work-dir", workDir, "--runner-id", "runner-existing"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if runtime.startOptions.RunnerID != "runner-existing" {
		t.Fatalf("explicit recovery options = %#v", runtime.startOptions)
	}

	runtime = &recordingLocalRunnerRuntime{}
	testseam.Swap(t, &localRunnerCommandRuntimeProvider, func() localRunnerCommandRuntime { return runtime })
	root = newRootCommandWithEngine(context.Background(), nil, false, true)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"deap", "runtime", "start-local", "--harness", "opencode", "--work-dir", workDir, "--endpoint-id", "endpoint-existing"})
	if err := root.Execute(); err == nil {
		t.Fatal("start-local accepted removed --endpoint-id")
	}
	if runtime.lastCall != "" {
		t.Fatalf("removed --endpoint-id reached runtime as %q", runtime.lastCall)
	}
}

func TestLocalRunnerStartLocalOpenCodeRejectsMissingOrBadWorkDirBeforeRuntime(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "missing harness", args: []string{"deap", "runtime", "start-local", "--work-dir", t.TempDir()}},
		{name: "missing work dir", args: []string{"deap", "runtime", "start-local", "--harness", "opencode"}},
		{name: "missing path", args: []string{"deap", "runtime", "start-local", "--harness", "opencode", "--work-dir", filepath.Join(t.TempDir(), "missing")}},
		{name: "file", args: []string{"deap", "runtime", "start-local", "--harness", "opencode", "--work-dir", file}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &recordingLocalRunnerRuntime{}
			testseam.Swap(t, &localRunnerCommandRuntimeProvider, func() localRunnerCommandRuntime { return runtime })
			root := newRootCommandWithEngine(context.Background(), nil, false, true)
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
			root.SetArgs(test.args)
			if err := root.Execute(); err == nil {
				t.Fatalf("start-local accepted %v", test.args)
			}
			if runtime.lastCall != "" {
				t.Fatalf("invalid command reached runtime as %q", runtime.lastCall)
			}
		})
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
		"deap", "runtime", "start-local",
		"--harness", "opencode",
		"--work-dir", t.TempDir(),
		"--local-agent-id", "agent-override",
		"--display-name", "Display override",
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
	if runtime.startOptions.AgentRef != "opencode" || runtime.startOptions.LocalAgentID != "agent-override" || runtime.startOptions.DisplayName != "Display override" || runtime.startOptions.OpenAPIBase != "" || runtime.startOptions.MaxConcurrent != 7 || runtime.startOptions.Streaming {
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
	root.SetArgs([]string{"deap", "runtime", "start-local", "--harness", "opencode", "--work-dir", t.TempDir()})
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
