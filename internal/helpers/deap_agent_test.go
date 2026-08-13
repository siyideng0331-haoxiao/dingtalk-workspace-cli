// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package helpers

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/corecmd/contractfinal"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/pkg/edition"
)

type deapAgentCall struct {
	productID string
	toolName  string
	args      map[string]any
}

type deapAgentCaller struct {
	dryRun bool
	calls  []deapAgentCall
}

func (c *deapAgentCaller) CallTool(_ context.Context, productID, toolName string, args map[string]any) (*edition.ToolResult, error) {
	c.calls = append(c.calls, deapAgentCall{productID: productID, toolName: toolName, args: args})
	return &edition.ToolResult{Content: []edition.ContentBlock{{Type: "text", Text: `{}`}}}, nil
}

func (*deapAgentCaller) Format() string { return "json" }
func (c *deapAgentCaller) DryRun() bool { return c.dryRun }
func (*deapAgentCaller) Fields() string { return "" }
func (*deapAgentCaller) JQ() string     { return "" }

func newDeapAgentTestTree(t *testing.T, dryRun bool) (*deapAgentCaller, *bytes.Buffer) {
	t.Helper()
	caller := &deapAgentCaller{dryRun: dryRun}
	InitDepsForTest(t, caller)
	out := &bytes.Buffer{}
	deps.Out.w = out
	return caller, out
}

func TestDevDeapAgentCommandTreeDeclaresNineDirectLeaves(t *testing.T) {
	newDeapAgentTestTree(t, false)
	dev := devHandler{}.Command(&captureRunner{})
	group, remaining, err := dev.Find([]string{"deap-agent"})
	if err != nil || len(remaining) != 0 {
		t.Fatalf("find dev deap-agent: group=%v remaining=%v err=%v", group, remaining, err)
	}
	wantLeaves := []string{
		"create", "detail", "list", "save-draft", "publish",
		"delete", "send-message", "run-status", "trace",
	}
	if got := len(group.Commands()); got != len(wantLeaves) {
		t.Fatalf("deap-agent direct child count = %d, want %d", got, len(wantLeaves))
	}
	for _, name := range wantLeaves {
		leaf, rest, findErr := group.Find([]string{name})
		if findErr != nil || len(rest) != 0 || leaf == group {
			t.Fatalf("find direct leaf %q: leaf=%v rest=%v err=%v", name, leaf, rest, findErr)
		}
		if leaf.HasSubCommands() {
			t.Errorf("dev deap-agent %s has an intermediate subtree", name)
		}
		if !leaf.Runnable() {
			t.Errorf("dev deap-agent %s is not runnable", name)
		}
		if leaf.Args == nil || leaf.Args(leaf, []string{"unexpected"}) == nil {
			t.Errorf("dev deap-agent %s must reject positional arguments", name)
		}
		final, ok := contractfinal.RuntimeContractFinal(leaf)
		if !ok || final.Identity == nil || final.Interface == nil || final.Safety == nil {
			t.Errorf("dev deap-agent %s has incomplete ContractFinal: %+v ok=%v", name, final, ok)
		}
	}
}

func TestDevDeapAgentHelpDescribesBuiltInEndpointResolution(t *testing.T) {
	newDeapAgentTestTree(t, false)
	dev := devHandler{}.Command(&captureRunner{})
	group, remaining, err := dev.Find([]string{"deap-agent"})
	if err != nil || len(remaining) != 0 {
		t.Fatalf("find dev deap-agent: group=%v remaining=%v err=%v", group, remaining, err)
	}
	if !strings.Contains(group.Long, "跟随当前 MCP 环境") {
		t.Fatal("deap-agent help must describe standard MCP environment resolution")
	}
	if strings.Contains(group.Long, "DINGTALK_DEAP_DEV_MCP_URL 显式配置") {
		t.Fatal("deap-agent help must not require a product-specific endpoint override")
	}
}

func TestDevDeapAgentAvailableLeavesRouteExactMCPTools(t *testing.T) {
	caller, _ := newDeapAgentTestTree(t, false)
	cases := []struct {
		leaf      string
		tool      string
		flags     map[string]string
		wantArgs  map[string]any
		confirmed bool
	}{
		{
			leaf: "create", tool: "create_digital_employee",
			flags: map[string]string{
				"name": "值班助手", "description": "处理值班问题",
				"dept-id": "dept-1", "dept-name": "值班组",
				"profile-json":   `{"employeeNo":"JSON-001","positionName":"值班员"}`,
				"employee-no":    "E001",
				"supervisor-uid": "supervisor-1",
				"response-mode":  "mention_only",
			},
			wantArgs: map[string]any{
				"name": "值班助手", "description": "处理值班问题",
				"deptId": "dept-1", "deptName": "值班组",
				"digitalTagEmployeeProfile": map[string]any{
					"employeeNo": "E001", "positionName": "值班员",
					"directSupervisorUid": "supervisor-1", "responseMode": "mention_only",
				},
			},
		},
		{
			leaf: "detail", tool: "get_digital_employee_detail",
			flags: map[string]string{"assistant-id": "assistant-1"},
			wantArgs: map[string]any{"assistantId": "assistant-1"},
		},
		{
			leaf: "list", tool: "list_digital_employees",
			flags: map[string]string{"keyword": "值班", "page": "2", "page-size": "101"},
			wantArgs: map[string]any{"keyword": "值班", "page": 2, "pageSize": 101},
		},
		{
			leaf: "save-draft", tool: "update_digital_employee_draft", confirmed: true,
			flags: map[string]string{
				"agent-uuid": "agent-1", "name": "新名称", "prompt": "你是值班助手",
				"profile-json":  `{"employeeNo":"E001","positionName":"旧岗位","responseMode":"mention_only"}`,
				"position-name": "值班员", "response-mode": "targeted_proactive",
			},
			wantArgs: map[string]any{
				"agentUuid": "agent-1", "name": "新名称", "prompt": "你是值班助手",
				"digitalTagEmployeeProfile": map[string]any{
					"employeeNo": "E001", "positionName": "值班员", "responseMode": "targeted_proactive",
				},
			},
		},
		{
			leaf: "publish", tool: "publish_digital_employee", confirmed: true,
			flags: map[string]string{"agent-uuid": "agent-1"},
			wantArgs: map[string]any{"agentUuid": "agent-1"},
		},
		{
			leaf: "delete", tool: "delete_digital_employee", confirmed: true,
			flags: map[string]string{"agent-uuid": "agent-1"},
			wantArgs: map[string]any{"agentUuid": "agent-1"},
		},
		{
			leaf: "send-message", tool: "send_de_message", confirmed: true,
			flags: map[string]string{"assistant-id": "agent-1", "query": "你好"},
			wantArgs: map[string]any{"assistantId": "agent-1", "query": "你好"},
		},
		{
			leaf: "run-status", tool: "query_de_run_status",
			flags: map[string]string{"assistant-id": "agent-1", "run-id": "run-1"},
			wantArgs: map[string]any{"assistantId": "agent-1", "runId": "run-1"},
		},
		{
			leaf: "run-status", tool: "query_de_run_status",
			flags: map[string]string{"assistant-id": "agent-1", "source-id": "open-message-1", "source-type": "im_message"},
			wantArgs: map[string]any{"assistantId": "agent-1", "sourceId": "open-message-1", "sourceType": "im_message"},
		},
		{
			leaf: "trace", tool: "query_de_trace",
			flags: map[string]string{"assistant-id": "agent-1", "run-id": "run-1"},
			wantArgs: map[string]any{"assistantId": "agent-1", "runId": "run-1"},
		},
		{
			leaf: "trace", tool: "query_de_trace",
			flags: map[string]string{"assistant-id": "agent-1", "source-id": "open-message-1", "source-type": "im_message"},
			wantArgs: map[string]any{"assistantId": "agent-1", "sourceId": "open-message-1", "sourceType": "im_message"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.leaf, func(t *testing.T) {
			caller.calls = nil
			dev := devHandler{}.Command(&captureRunner{})
			leaf, _, err := dev.Find([]string{"deap-agent", tc.leaf})
			if err != nil {
				t.Fatal(err)
			}
			if tc.confirmed {
				leaf.Flags().Bool("yes", false, "test confirmation")
				tc.flags["yes"] = "true"
			}
			for name, value := range tc.flags {
				if setErr := leaf.Flags().Set(name, value); setErr != nil {
					t.Fatal(setErr)
				}
			}
			if runErr := leaf.RunE(leaf, nil); runErr != nil {
				t.Fatalf("RunE() error = %v", runErr)
			}
			if len(caller.calls) != 1 {
				t.Fatalf("MCP call count = %d, want 1", len(caller.calls))
			}
			call := caller.calls[0]
			if call.productID != "deap-dev" || call.toolName != tc.tool {
				t.Fatalf("route = %s/%s, want deap-dev/%s", call.productID, call.toolName, tc.tool)
			}
			if !reflect.DeepEqual(call.args, tc.wantArgs) {
				t.Fatalf("args = %#v, want %#v", call.args, tc.wantArgs)
			}
			for _, forbidden := range []string{"identity", "corpId", "userId", "orgId", "agentType"} {
				if _, ok := call.args[forbidden]; ok {
					t.Errorf("trusted or retired field %s leaked into MCP arguments", forbidden)
				}
			}
		})
	}
}

func TestDevDeapAgentConstraintsFailBeforeMCP(t *testing.T) {
	caller, _ := newDeapAgentTestTree(t, false)
	cases := []struct {
		leaf    string
		flags   map[string]string
		wantErr string
	}{
		{leaf: "run-status", flags: map[string]string{"run-id": "run-1"}, wantErr: "assistant-id"},
		{leaf: "run-status", flags: map[string]string{"assistant-id": "agent-1"}, wantErr: "run-id"},
		{leaf: "run-status", flags: map[string]string{"assistant-id": "agent-1", "run-id": "run-1", "source-id": "src-1", "source-type": "im_message"}, wantErr: "run-id"},
		{leaf: "run-status", flags: map[string]string{"assistant-id": "agent-1", "source-id": "src-1"}, wantErr: "--source-type"},
		{leaf: "run-status", flags: map[string]string{"assistant-id": "agent-1", "run-id": "run-1", "source-type": "im_message"}, wantErr: "--source-type 只能随"},
		{leaf: "trace", flags: map[string]string{"run-id": "run-1"}, wantErr: "assistant-id"},
		{leaf: "trace", flags: map[string]string{"assistant-id": "agent-1"}, wantErr: "run-id"},
		{leaf: "trace", flags: map[string]string{"assistant-id": "agent-1", "run-id": "run-1", "source-id": "src-1", "source-type": "im_message"}, wantErr: "run-id"},
		{leaf: "trace", flags: map[string]string{"assistant-id": "agent-1", "source-id": "src-1"}, wantErr: "--source-type"},
		{leaf: "list", flags: map[string]string{"page": "0"}, wantErr: "--page 不能小于 1"},
		{leaf: "list", flags: map[string]string{"page-size": "0"}, wantErr: "--page-size 不能小于 1"},
		{leaf: "create", flags: map[string]string{
			"name": "值班助手", "description": "处理值班问题", "dept-id": "dept-1", "dept-name": "值班组",
			"profile-json": `{"tag":"forbidden"}`,
		}, wantErr: "不接受字段"},
		{leaf: "create", flags: map[string]string{
			"name": "值班助手", "description": "处理值班问题", "dept-id": "dept-1", "dept-name": "值班组",
			"response-mode": "always_reply",
		}, wantErr: "--response-mode"},
		{leaf: "save-draft", flags: map[string]string{
			"agent-uuid": "agent-1", "employee-no": strings.Repeat("E", 65),
		}, wantErr: "最多允许 64"},
		{leaf: "save-draft", flags: map[string]string{
			"agent-uuid": "agent-1", "position-name": strings.Repeat("岗", 129),
		}, wantErr: "最多允许 128"},
		{leaf: "save-draft", flags: map[string]string{
			"agent-uuid": "agent-1", "prompt": strings.Repeat("提", 5001),
		}, wantErr: "最多允许 5000"},
	}
	for _, tc := range cases {
		t.Run(tc.leaf+tc.wantErr, func(t *testing.T) {
			caller.calls = nil
			dev := devHandler{}.Command(&captureRunner{})
			leaf, _, err := dev.Find([]string{"deap-agent", tc.leaf})
			if err != nil {
				t.Fatal(err)
			}
			for name, value := range tc.flags {
				if setErr := leaf.Flags().Set(name, value); setErr != nil {
					t.Fatal(setErr)
				}
			}
			runErr := leaf.RunE(leaf, nil)
			if runErr == nil || !strings.Contains(runErr.Error(), tc.wantErr) {
				t.Fatalf("RunE() error = %v, want containing %q", runErr, tc.wantErr)
			}
			if len(caller.calls) != 0 {
				t.Fatalf("invalid input made %d MCP call(s)", len(caller.calls))
			}
		})
	}
}

func TestDevDeapAgentRemovesRetiredFlagsAndKeepsIdentityHidden(t *testing.T) {
	caller, _ := newDeapAgentTestTree(t, false)
	dev := devHandler{}.Command(&captureRunner{})
	create, _, err := dev.Find([]string{"deap-agent", "create"})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"org-id", "user-id", "agent-type", "developers-json"} {
		if flag := create.Flags().Lookup(forbidden); flag != nil {
			t.Fatalf("forbidden identity/retired flag --%s is exposed", forbidden)
		}
	}
	list, _, err := dev.Find([]string{"deap-agent", "list"})
	if err != nil {
		t.Fatal(err)
	}
	if flag := list.Flags().Lookup("sort-by"); flag != nil {
		t.Fatal("retired --sort-by is exposed")
	}
	send, _, err := dev.Find([]string{"deap-agent", "send-message"})
	if err != nil {
		t.Fatal(err)
	}
	if flag := send.Flags().Lookup("content"); flag != nil {
		t.Fatal("retired --content is exposed; current MCP input is --query")
	}
	trace, _, err := dev.Find([]string{"deap-agent", "trace"})
	if err != nil {
		t.Fatal(err)
	}
	if flag := trace.Flags().Lookup("trace-id"); flag != nil {
		t.Fatal("retired --trace-id is exposed; current MCP input uses the run locator")
	}
	save, _, err := dev.Find([]string{"deap-agent", "save-draft"})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"developers-json", "prompt-config-json", "model-config-json", "knowledge-config-json",
		"memory-config-json", "selected-skills-json", "deleted-skills-json", "scopes-json",
		"shortcuts-json", "scheduled-task-json",
	} {
		if flag := save.Flags().Lookup(forbidden); flag != nil {
			t.Fatalf("retired save-draft flag --%s is exposed", forbidden)
		}
	}

	for name, value := range map[string]string{
		"name": "值班助手", "description": "处理值班问题",
		"dept-id": "dept-1", "dept-name": "值班组",
	} {
		if setErr := create.Flags().Set(name, value); setErr != nil {
			t.Fatal(setErr)
		}
	}
	if err := create.RunE(create, nil); err != nil {
		t.Fatalf("create RunE() error = %v", err)
	}
	if len(caller.calls) != 1 {
		t.Fatalf("MCP call count = %d, want 1", len(caller.calls))
	}
	call := caller.calls[0]
	if call.productID != "deap-dev" || call.toolName != "create_digital_employee" {
		t.Fatalf("route = %s/%s, want deap-dev/create_digital_employee", call.productID, call.toolName)
	}
	want := map[string]any{
		"name": "值班助手", "description": "处理值班问题",
		"deptId": "dept-1", "deptName": "值班组",
	}
	if !reflect.DeepEqual(call.args, want) {
		t.Fatalf("create args = %#v, want %#v", call.args, want)
	}
	for _, forbidden := range []string{"identity", "corpId", "orgId", "userId", "agentType"} {
		if _, ok := call.args[forbidden]; ok {
			t.Fatalf("trusted identity field %s leaked into MCP arguments", forbidden)
		}
	}
}

func TestDevDeapAgentHelpMatchesCurrentMCPInputs(t *testing.T) {
	newDeapAgentTestTree(t, false)
	dev := devHandler{}.Command(&captureRunner{})

	send, _, err := dev.Find([]string{"deap-agent", "send-message"})
	if err != nil {
		t.Fatal(err)
	}
	if flag := send.Flags().Lookup("query"); flag == nil {
		t.Fatal("send-message must expose the required MCP query input")
	}
	if !strings.Contains(send.Long, "query") || !strings.Contains(send.Long, "不可撤回") {
		t.Fatalf("send-message help does not match the current MCP tool: %q", send.Long)
	}

	publish, _, err := dev.Find([]string{"deap-agent", "publish"})
	if err != nil {
		t.Fatal(err)
	}
	if flag := publish.Flags().Lookup("allow-join-group"); flag == nil || flag.DefValue != "false" {
		t.Fatalf("allow-join-group default = %v, current MCP declares an optional boolean without a default", flag)
	}

	for _, name := range []string{"run-status", "trace"} {
		command, _, findErr := dev.Find([]string{"deap-agent", name})
		if findErr != nil {
			t.Fatal(findErr)
		}
		if flag := command.Flags().Lookup("assistant-id"); flag == nil {
			t.Fatalf("%s is missing MCP input --assistant-id", name)
		}
		for _, flagName := range []string{"run-id", "source-id", "source-type"} {
			if flag := command.Flags().Lookup(flagName); flag == nil {
				t.Fatalf("%s is missing MCP input --%s", name, flagName)
			}
		}
		if !strings.Contains(command.Long, "--run-id") || !strings.Contains(command.Long, "--source-id") {
			t.Fatalf("%s help does not explain the current MCP run locator: %q", name, command.Long)
		}
	}
}

func TestDevDeapAgentHelpExplainsFullReplacementAndTraceAuthorization(t *testing.T) {
	caller, _ := newDeapAgentTestTree(t, false)
	dev := devHandler{}.Command(&captureRunner{})
	save, _, err := dev.Find([]string{"deap-agent", "save-draft"})
	if err != nil {
		t.Fatal(err)
	}
	save.SetOut(io.Discard)
	if helpErr := save.Help(); helpErr != nil {
		t.Fatal(helpErr)
	}
	if !strings.Contains(save.Long, "全量覆写") || !strings.Contains(save.Long, "detail") || strings.Contains(save.Long, "export-draft") {
		t.Fatalf("save-draft help does not explain read-before-write full replacement: %q", save.Long)
	}
	trace, _, err := dev.Find([]string{"deap-agent", "trace"})
	if err != nil {
		t.Fatal(err)
	}
	final, ok := contractfinal.RuntimeContractFinal(trace)
	if !ok || final.Interface == nil || final.Interface.Availability != "available" {
		t.Fatalf("trace ContractFinal interface = %+v ok=%v, want available", final.Interface, ok)
	}
	if strings.Contains(trace.Long, "暂不可") || strings.Contains(trace.Long, "fail-closed") {
		t.Fatalf("trace help still claims unavailable: %q", trace.Long)
	}
	if !strings.Contains(trace.Long, "授权") {
		t.Fatalf("trace help does not explain server-side authorization: %q", trace.Long)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("help inspection made %d remote call(s)", len(caller.calls))
	}
}
