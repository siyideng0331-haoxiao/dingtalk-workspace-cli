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
	"github.com/spf13/cobra"
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

// deapFindLeaf 在 `deap manage` / `deap observe` 两个子组里找叶子。
//
// 为何不让用例自己写子组名：绝大多数用例关心的是“这个叶子的行为”而不是“它挂在哪个
// 子组”；把子组名写进每个用例会让以后调整归类（如新增子组、或把某命令从管理态
// 移到观测态）逐处改。归类本身由 TreeSplitsManageAndObserve 单独钉住。
func deapFindLeaf(t *testing.T, root *cobra.Command, leaf string) *cobra.Command {
	t.Helper()
	for _, group := range []string{"manage", "observe"} {
		cmd, remaining, err := root.Find([]string{group, leaf})
		if err == nil && len(remaining) == 0 && cmd.Name() == leaf {
			return cmd
		}
	}
	t.Fatalf("deap leaf %q not found under manage/observe", leaf)
	return nil
}

// TestDeapCommandTreeSplitsManageAndObserve 钉住顶级 `dws deap` 的两子组归类。
//
// 为何钉归类而不只钉叶子集合：管理态含不可逆写操作，观测态全是只读；两者混放会
// 让调用方（含 Agent）失去“这一类命令安全属性相同”这个判断依据。
func TestDeapCommandTreeSplitsManageAndObserve(t *testing.T) {
	newDeapAgentTestTree(t, false)
	root := deapHandler{}.Command(&captureRunner{})

	wantGroups := map[string][]string{
		"manage": {
			"create", "detail", "list", "save-draft", "publish", "delete",
			"add-internal-sub-agent", "remove-internal-sub-agent",
			"add-a2a-sub-agent", "remove-a2a-sub-agent",
		},
		"observe": {"run-status", "trace"},
	}
	if got := len(root.Commands()); got != len(wantGroups) {
		t.Fatalf("deap direct child count = %d, want %d (only manage/observe)", got, len(wantGroups))
	}
	for groupName, wantLeaves := range wantGroups {
		group, remaining, err := root.Find([]string{groupName})
		if err != nil || len(remaining) != 0 {
			t.Fatalf("find deap %s: group=%v remaining=%v err=%v", groupName, group, remaining, err)
		}
		if got := len(group.Commands()); got != len(wantLeaves) {
			t.Fatalf("deap %s leaf count = %d, want %d", groupName, got, len(wantLeaves))
		}
		for _, name := range wantLeaves {
			leaf, rest, findErr := group.Find([]string{name})
			if findErr != nil || len(rest) != 0 || leaf == group {
				t.Fatalf("find deap %s %q: leaf=%v rest=%v err=%v", groupName, name, leaf, rest, findErr)
			}
			if leaf.HasSubCommands() {
				t.Errorf("deap %s %s has an intermediate subtree", groupName, name)
			}
			if !leaf.Runnable() {
				t.Errorf("deap %s %s is not runnable", groupName, name)
			}
			if leaf.Args == nil || leaf.Args(leaf, []string{"unexpected"}) == nil {
				t.Errorf("deap %s %s must reject positional arguments", groupName, name)
			}
			final, ok := contractfinal.RuntimeContractFinal(leaf)
			if !ok || final.Identity == nil || final.Interface == nil || final.Safety == nil {
				t.Errorf("deap %s %s has incomplete ContractFinal: %+v ok=%v", groupName, name, final, ok)
			}
		}
	}
}

func TestDeapHelpDescribesBuiltInEndpointResolution(t *testing.T) {
	newDeapAgentTestTree(t, false)
	root := deapHandler{}.Command(&captureRunner{})
	if !strings.Contains(root.Long, "跟随当前 MCP 环境") {
		t.Fatal("deap help must describe standard MCP environment resolution")
	}
	if strings.Contains(root.Long, "DINGTALK_DEAP_DEV_MCP_URL 显式配置") {
		t.Fatal("deap help must not require a product-specific endpoint override")
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
			flags:    map[string]string{"assistant-id": "assistant-1"},
			wantArgs: map[string]any{"assistantId": "assistant-1"},
		},
		{
			leaf: "list", tool: "list_digital_employees",
			flags:    map[string]string{"keyword": "值班", "page": "2", "page-size": "101"},
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
			flags:    map[string]string{"agent-uuid": "agent-1"},
			wantArgs: map[string]any{"agentUuid": "agent-1"},
		},
		{
			leaf: "delete", tool: "delete_digital_employee", confirmed: true,
			flags:    map[string]string{"agent-uuid": "agent-1"},
			wantArgs: map[string]any{"agentUuid": "agent-1"},
		},
		{
			leaf: "add-internal-sub-agent", tool: "add_de_internal_sub_agent", confirmed: true,
			flags: map[string]string{
				"agent-uuid": "agent-1", "sub-agent-uuid": "internal-agent-1",
			},
			wantArgs: map[string]any{
				"agentUuid": "agent-1", "subAgentUuid": "internal-agent-1",
			},
		},
		{
			leaf: "remove-internal-sub-agent", tool: "remove_de_internal_sub_agent", confirmed: true,
			flags: map[string]string{
				"agent-uuid": "agent-1", "sub-agent-instance-id": "internal-instance-1",
			},
			wantArgs: map[string]any{
				"agentUuid": "agent-1", "subAgentInstanceId": "internal-instance-1",
			},
		},
		{
			leaf: "add-a2a-sub-agent", tool: "add_de_a2a_sub_agent", confirmed: true,
			flags: map[string]string{
				"agent-uuid": "agent-1", "name": "外部客服", "description": "外部 A2A 客服",
				"agent-card-url": "https://example.com/.well-known/agent-card.json",
			},
			wantArgs: map[string]any{
				"agentUuid": "agent-1", "name": "外部客服", "description": "外部 A2A 客服",
				"agentCardUrl": "https://example.com/.well-known/agent-card.json",
			},
		},
		{
			leaf: "remove-a2a-sub-agent", tool: "remove_de_a2a_sub_agent", confirmed: true,
			flags: map[string]string{
				"agent-uuid": "agent-1", "sub-agent-instance-id": "a2a-instance-1",
			},
			wantArgs: map[string]any{
				"agentUuid": "agent-1", "subAgentInstanceId": "a2a-instance-1",
			},
		},
		{
			leaf: "run-status", tool: "query_de_run_status",
			flags:    map[string]string{"assistant-id": "agent-1", "source-id": "open-message-1", "source-type": "im_message"},
			wantArgs: map[string]any{"assistantId": "agent-1", "sourceId": "open-message-1", "sourceType": "im_message"},
		},
		{
			leaf: "trace", tool: "query_de_trace",
			flags:    map[string]string{"assistant-id": "agent-1", "source-id": "open-message-1", "source-type": "trigger_rule"},
			wantArgs: map[string]any{"assistantId": "agent-1", "sourceId": "open-message-1", "sourceType": "trigger_rule"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.leaf, func(t *testing.T) {
			caller.calls = nil
			root := deapHandler{}.Command(&captureRunner{})
			leaf := deapFindLeaf(t, root, tc.leaf)
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
		{leaf: "run-status", flags: map[string]string{"source-id": "src-1", "source-type": "im_message"}, wantErr: "assistant-id"},
		{leaf: "run-status", flags: map[string]string{"assistant-id": "agent-1"}, wantErr: "source-id"},
		{leaf: "run-status", flags: map[string]string{"assistant-id": "agent-1", "source-id": "src-1"}, wantErr: "source-type"},
		{leaf: "trace", flags: map[string]string{"source-id": "src-1", "source-type": "im_message"}, wantErr: "assistant-id"},
		{leaf: "trace", flags: map[string]string{"assistant-id": "agent-1"}, wantErr: "source-id"},
		{leaf: "trace", flags: map[string]string{"assistant-id": "agent-1", "source-id": "src-1"}, wantErr: "source-type"},
		{leaf: "list", flags: map[string]string{"page": "0"}, wantErr: "--page 不能小于 1"},
		{leaf: "list", flags: map[string]string{"page-size": "0"}, wantErr: "--page-size 不能小于 1"},
		{leaf: "add-internal-sub-agent", flags: map[string]string{"agent-uuid": "agent-1"}, wantErr: "sub-agent-uuid"},
		{leaf: "remove-internal-sub-agent", flags: map[string]string{"agent-uuid": "agent-1"}, wantErr: "sub-agent-instance-id"},
		{leaf: "add-a2a-sub-agent", flags: map[string]string{
			"agent-uuid": "agent-1", "name": "外部客服", "description": "外部 A2A 客服",
		}, wantErr: "agent-card-url"},
		{leaf: "remove-a2a-sub-agent", flags: map[string]string{"agent-uuid": "agent-1"}, wantErr: "sub-agent-instance-id"},
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
			root := deapHandler{}.Command(&captureRunner{})
			leaf := deapFindLeaf(t, root, tc.leaf)
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
	root := deapHandler{}.Command(&captureRunner{})
	create := deapFindLeaf(t, root, "create")
	for _, forbidden := range []string{"org-id", "user-id", "agent-type", "developers-json"} {
		if flag := create.Flags().Lookup(forbidden); flag != nil {
			t.Fatalf("forbidden identity/retired flag --%s is exposed", forbidden)
		}
	}
	list := deapFindLeaf(t, root, "list")
	if flag := list.Flags().Lookup("sort-by"); flag != nil {
		t.Fatal("retired --sort-by is exposed")
	}
	// send-message 已下线：不能用 deapFindLeaf（它找不到就 Fatal，语义刚好反了），
	// 直接断言两个子组里都没有它。
	for _, group := range []string{"manage", "observe"} {
		if cmd, _, findErr := root.Find([]string{group, "send-message"}); findErr == nil &&
			cmd != nil && cmd.Name() == "send-message" {
			t.Fatalf("retired send-message command is exposed under %s; 推送能力已从观测接口移除", group)
		}
	}
	trace := deapFindLeaf(t, root, "trace")
	if flag := trace.Flags().Lookup("trace-id"); flag != nil {
		t.Fatal("retired --trace-id is exposed; current MCP input uses the run locator")
	}
	if flag := trace.Flags().Lookup("run-id"); flag != nil {
		t.Fatal("retired --run-id is exposed; 调用方拿不到 runId，只能按来源定位")
	}
	save := deapFindLeaf(t, root, "save-draft")
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
	root := deapHandler{}.Command(&captureRunner{})

	publish := deapFindLeaf(t, root, "publish")
	if flag := publish.Flags().Lookup("allow-join-group"); flag == nil || flag.DefValue != "false" {
		t.Fatalf("allow-join-group default = %v, current MCP declares an optional boolean without a default", flag)
	}

	for _, name := range []string{"run-status", "trace"} {
		command := deapFindLeaf(t, root, name)
		if flag := command.Flags().Lookup("assistant-id"); flag == nil {
			t.Fatalf("%s is missing MCP input --assistant-id", name)
		}
		for _, flagName := range []string{"source-id", "source-type"} {
			if flag := command.Flags().Lookup(flagName); flag == nil {
				t.Fatalf("%s is missing MCP input --%s", name, flagName)
			}
		}
		if !strings.Contains(command.Long, "--source-id") || !strings.Contains(command.Long, "--source-type") {
			t.Fatalf("%s help does not explain the current MCP run locator: %q", name, command.Long)
		}
	}
}

func TestDevDeapAgentHelpExplainsFullReplacementAndTraceAuthorization(t *testing.T) {
	caller, _ := newDeapAgentTestTree(t, false)
	root := deapHandler{}.Command(&captureRunner{})
	save := deapFindLeaf(t, root, "save-draft")
	save.SetOut(io.Discard)
	if helpErr := save.Help(); helpErr != nil {
		t.Fatal(helpErr)
	}
	if !strings.Contains(save.Long, "全量覆写") || !strings.Contains(save.Long, "detail") || strings.Contains(save.Long, "export-draft") {
		t.Fatalf("save-draft help does not explain read-before-write full replacement: %q", save.Long)
	}
	trace := deapFindLeaf(t, root, "trace")
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
