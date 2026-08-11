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

// Package deap holds the built-in DEAP 数字员工 shortcuts. DEAP is the platform
// that hosts digital employees (数字员工); this package covers their management
// lifecycle plus execution observability.
//
// Tool names and parameter keys mirror the DEAP HSF interfaces registered on the
// MCP platform (DeapDigitalEmployeeRemoteService / DeapObservabilityRemoteService).
// All tools route to the "deap" MCP server via Product.
//
// Identity: DEAP does not use API keys. Every tool takes orgId + userId, which the
// server side turns back into a user context so permission checks match the DEAP
// console exactly — 管理者 sees everything for an agent, 使用者 only sees the runs
// they themselves triggered. Both values come from the CLI session, so no identity
// flags are exposed here.
package deap

import (
	"strings"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/corecmd"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/corecmd/contract"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/shortcut"
)

// readSafety is the shared safety declaration for read-only shortcuts.
func readSafety() contract.SafetySpec {
	return contract.SafetySpec{
		Effect: "read", Risk: "low",
		Confirmation: "not_required", Idempotency: "idempotent",
	}
}

// writeSafety is the shared safety declaration for shortcuts that change state.
func writeSafety() contract.SafetySpec {
	return contract.SafetySpec{
		Effect: "write", Risk: "medium",
		Confirmation: "user_required", Idempotency: "unknown",
	}
}

// destructiveSafety marks the irreversible shortcut. Kept separate from
// writeSafety so the higher risk is visible in the published contract.
func destructiveSafety() contract.SafetySpec {
	return contract.SafetySpec{
		Effect: "destructive", Risk: "high",
		Confirmation: "user_required", Idempotency: "unknown",
	}
}

// identity builds the tool contract identity from the leaf command name.
//
// The Name is derived as "shortcut_<leaf with - replaced by _>" rather than the
// underlying MCP tool name: the atomic helper layer already owns the canonical
// path "deap.<tool>", and reusing it here would collide when the Schema registry
// collects identity specs. Mirrors reviewedChatShortcutContract's derivation.
func identity(leaf string) contract.ToolIdentitySpec {
	name := "shortcut_" + strings.ReplaceAll(strings.TrimPrefix(leaf, "+"), "-", "_")
	return contract.ToolIdentitySpec{
		ProductID:      "deap",
		Name:           name,
		CanonicalPath:  "deap." + name,
		CLIPath:        "deap " + leaf,
		PrimaryCLIPath: "deap " + leaf,
	}
}

// compositeInterface is the shared interface spec: these shortcuts own their own
// validation and output projection rather than pinning one MCP interface_ref.
func compositeInterface() *contract.InterfaceSpec {
	return &contract.InterfaceSpec{
		Mode:         "composite",
		Availability: "available",
		Reason:       "Reviewed built-in shortcut adapter: the executable CLI owns validation, output projection, and confirmation; the complete command contract is not represented by one pinned MCP interface_ref.",
	}
}

// ===== 生命周期 =====

// EmployeeList lists digital employees visible to the caller.
var EmployeeList = shortcut.Shortcut{
	Service: "deap", Command: "+employee-list", Product: "deap",
	Description: "查询数字员工列表",
	Intent:      "当你想看当前身份能管理或参与的数字员工有哪些、或需要拿到某个数字员工的 assistantId 以便后续查详情、发布或观测时使用；可按关键词模糊搜索员工名称/岗位/工号，只读不产生副作用。可见范围与 DEAP 后台一致：管理员看本租户全部，非管理员只看自己参与的。",
	Risk:        shortcut.RiskRead,
	Safety:      readSafety(),
	Contract: corecmd.ContractDecl{
		Identity:    identity("+employee-list"),
		Description: "查询数字员工列表",
		Interface:   compositeInterface(),
		Selection: contract.SelectionSpec{
			AgentSummary: "查询数字员工列表",
			UseWhen:      []string{"当你想看有哪些数字员工、或需要先拿到 assistantId 再做后续操作时使用；支持按名称/岗位/工号模糊搜索。"},
			AvoidWhen:    []string{"要查的是 DEAP 助理（agentType=1）而非数字员工时，本命令查不到"},
			Examples:     []string{"dws deap +employee-list", `dws deap +employee-list --keyword "招聘"`},
		},
	},
	Flags: []shortcut.Flag{
		{Name: "keyword", Type: shortcut.FlagString, Desc: "模糊搜索词，匹配员工名称 / 岗位名称 / 工号 (可选)"},
		{Name: "page", Type: shortcut.FlagInt, Default: "1", Desc: "页码 (可选，默认 1)"},
		{Name: "page-size", Type: shortcut.FlagInt, Default: "20", Desc: "每页条数 (可选，默认 20)"},
	},
	Tips: []string{`dws deap +employee-list --keyword "招聘" --page-size 50`},
	Execute: func(rt *shortcut.RuntimeContext) error {
		params := map[string]any{
			"page":     rt.Int("page"),
			"pageSize": rt.Int("page-size"),
		}
		if v := rt.Str("keyword"); v != "" {
			params["keyword"] = v
		}
		return rt.CallMCP("list_digital_employees", params)
	},
}

// EmployeeDetail reads one digital employee including its profile meta.
var EmployeeDetail = shortcut.Shortcut{
	Service: "deap", Command: "+employee-detail", Product: "deap",
	Description: "查询数字员工详情",
	Intent:      "当你要查看某个数字员工的完整配置（名称、描述、岗位、响应模式、人设提示词等）、或在改配置前读回全量以便增量修改时使用；也可用它确认某个 ID 是否真的是数字员工——不是数字员工会直接报不存在。只读操作。",
	Risk:        shortcut.RiskRead,
	Safety:      readSafety(),
	Contract: corecmd.ContractDecl{
		Identity:    identity("+employee-detail"),
		Description: "查询数字员工详情",
		Interface:   compositeInterface(),
		Selection: contract.SelectionSpec{
			AgentSummary: "查询数字员工详情",
			UseWhen:      []string{"当你要看某个数字员工的完整配置，或在保存草稿前读回全量避免覆写丢字段时使用。"},
			AvoidWhen:    []string{"只需要列表概览时用 +employee-list，避免逐个查详情"},
			Examples:     []string{"dws deap +employee-detail --assistant <assistantId>"},
		},
	},
	Flags: []shortcut.Flag{
		{Name: "assistant", Type: shortcut.FlagString, Desc: "数字员工 ID (assistantId)", Required: true},
	},
	Tips: []string{"dws deap +employee-detail --assistant <assistantId>"},
	Execute: func(rt *shortcut.RuntimeContext) error {
		return rt.CallMCP("get_digital_employee", map[string]any{
			"assistantId": rt.Str("assistant"),
		})
	},
}

// EmployeeCreate creates a digital employee in draft state.
var EmployeeCreate = shortcut.Shortcut{
	Service: "deap", Command: "+employee-create", Product: "deap",
	Description: "创建数字员工（草稿态）",
	Intent:      "当你要新建一个数字员工时使用。创建出来是草稿态、还不会响应任何消息，需要再执行 +employee-publish 才生效。名称、描述、组织编码与组织名称必填；岗位名称与响应模式可留到之后再填，但不补齐无法发布。",
	Risk:        shortcut.RiskWrite,
	Safety:      writeSafety(),
	Contract: corecmd.ContractDecl{
		Identity:    identity("+employee-create"),
		Description: "创建数字员工（草稿态）",
		Interface:   compositeInterface(),
		Selection: contract.SelectionSpec{
			AgentSummary: "创建数字员工（草稿态）",
			UseWhen:      []string{"当你要新建数字员工时使用；创建后为草稿态，需再发布才生效。"},
			AvoidWhen:    []string{"要修改已有数字员工时用 +employee-save-draft，不要重复创建"},
			Examples: []string{
				`dws deap +employee-create --name "招聘助手小钉" --description "负责简历初筛与面试安排" --org-code HR --org-name "人力资源部"`,
			},
		},
	},
	Flags: []shortcut.Flag{
		{Name: "name", Type: shortcut.FlagString, Desc: "员工名称", Required: true},
		{Name: "description", Type: shortcut.FlagString, Desc: "职责描述", Required: true},
		{Name: "org-code", Type: shortcut.FlagString, Desc: "归属组织编码", Required: true},
		{Name: "org-name", Type: shortcut.FlagString, Desc: "归属组织名称", Required: true},
		{Name: "icon", Type: shortcut.FlagString, Desc: "头像 (可选，但发布前必填)"},
		{Name: "employee-no", Type: shortcut.FlagString, Desc: "工号 (可选)"},
		{Name: "position-name", Type: shortcut.FlagString, Desc: "岗位名称 (可选，但发布前必填)"},
		{Name: "supervisor-uid", Type: shortcut.FlagString, Desc: "直属上级钉钉 uid (可选)"},
		{Name: "response-mode", Type: shortcut.FlagString, Enum: []string{"mention_only", "targeted_proactive"},
			Desc: "响应模式：mention_only 仅被@时响应 / targeted_proactive 定向主动响应 (可选，但发布前必填)"},
	},
	Tips: []string{
		`dws deap +employee-create --name "招聘助手小钉" --description "简历初筛" --org-code HR --org-name "人力资源部" --position-name "招聘专员" --response-mode mention_only`,
	},
	Execute: func(rt *shortcut.RuntimeContext) error {
		params := map[string]any{
			"name":        rt.Str("name"),
			"description": rt.Str("description"),
			"orgCode":     rt.Str("org-code"),
			"orgName":     rt.Str("org-name"),
		}
		if v := rt.Str("icon"); v != "" {
			params["icon"] = v
		}
		if profile := buildProfile(rt); len(profile) > 0 {
			params["digitalTagEmployeeProfile"] = profile
		}
		return rt.CallMCP("create_digital_employee", params)
	},
}

// EmployeeSaveDraft saves the draft configuration without publishing.
var EmployeeSaveDraft = shortcut.Shortcut{
	Service: "deap", Command: "+employee-save-draft", Product: "deap",
	Description: "保存数字员工草稿（不发布）",
	Intent:      "当你要修改数字员工的配置但还不想让改动生效时使用。注意这是全量覆写——没传的字段会被清空，所以做增量修改必须先用 +employee-detail 读回全量、改动其中几项后整体回传，否则会抹掉原有配置。保存后需再执行 +employee-publish 才生效。",
	Risk:        shortcut.RiskWrite,
	Safety:      writeSafety(),
	Contract: corecmd.ContractDecl{
		Identity:    identity("+employee-save-draft"),
		Description: "保存数字员工草稿（不发布）",
		Interface:   compositeInterface(),
		Selection: contract.SelectionSpec{
			AgentSummary: "保存数字员工草稿（不发布）",
			UseWhen:      []string{"当你要改数字员工配置时使用；先读详情再整体回传，避免覆写丢字段。保存后需再发布。"},
			AvoidWhen:    []string{"只想让已保存的草稿生效时直接用 +employee-publish，无需重复保存"},
			Examples:     []string{`dws deap +employee-save-draft --assistant <assistantId> --prompt "你是一名招聘助手"`},
		},
	},
	Flags: []shortcut.Flag{
		{Name: "assistant", Type: shortcut.FlagString, Desc: "数字员工 ID (assistantId)", Required: true},
		{Name: "name", Type: shortcut.FlagString, Desc: "员工名称"},
		{Name: "description", Type: shortcut.FlagString, Desc: "职责描述"},
		{Name: "org-code", Type: shortcut.FlagString, Desc: "归属组织编码"},
		{Name: "org-name", Type: shortcut.FlagString, Desc: "归属组织名称"},
		{Name: "icon", Type: shortcut.FlagString, Desc: "头像 (发布前必填)"},
		{Name: "prompt", Type: shortcut.FlagString, Desc: "人设提示词 (发布前必填)"},
		{Name: "employee-no", Type: shortcut.FlagString, Desc: "工号"},
		{Name: "position-name", Type: shortcut.FlagString, Desc: "岗位名称 (发布前必填)"},
		{Name: "supervisor-uid", Type: shortcut.FlagString, Desc: "直属上级钉钉 uid"},
		{Name: "response-mode", Type: shortcut.FlagString, Enum: []string{"mention_only", "targeted_proactive"},
			Desc: "响应模式 (发布前必填)"},
	},
	Tips: []string{
		"# 全量覆写：先读回详情，再把要改的项一起传回",
		`dws deap +employee-save-draft --assistant <assistantId> --name "招聘助手小钉" --prompt "你是一名招聘助手"`,
	},
	Execute: func(rt *shortcut.RuntimeContext) error {
		params := map[string]any{"agentUuid": rt.Str("assistant")}
		for flag, key := range map[string]string{
			"name":        "name",
			"description": "description",
			"org-code":    "orgCode",
			"org-name":    "orgName",
			"icon":        "icon",
			"prompt":      "prompt",
		} {
			if v := rt.Str(flag); v != "" {
				params[key] = v
			}
		}
		if profile := buildProfile(rt); len(profile) > 0 {
			params["digitalTagEmployeeProfile"] = profile
		}
		return rt.CallMCP("save_digital_employee_draft", params)
	},
}

// EmployeePublish makes the current draft take effect.
var EmployeePublish = shortcut.Shortcut{
	Service: "deap", Command: "+employee-publish", Product: "deap",
	Description: "发布数字员工（使当前草稿生效）",
	Intent:      "当你要让数字员工的当前草稿正式生效、开始响应消息时使用。发布只消费 ID 与是否允许入群，其余配置一律以已保存的草稿为准——要改配置先执行 +employee-save-draft。发布前有一组必填校验（名称、头像、描述、组织编码、岗位名称、响应模式、人设提示词），缺任一项会被拒且不产生任何副作用，补齐后可重试。",
	Risk:        shortcut.RiskWrite,
	Safety:      writeSafety(),
	Contract: corecmd.ContractDecl{
		Identity:    identity("+employee-publish"),
		Description: "发布数字员工（使当前草稿生效）",
		Interface:   compositeInterface(),
		Selection: contract.SelectionSpec{
			AgentSummary: "发布数字员工（使当前草稿生效）",
			UseWhen:      []string{"当草稿已配置完整、要让数字员工正式生效时使用。"},
			AvoidWhen:    []string{"配置还没保存时先用 +employee-save-draft；发布不携带配置"},
			Examples:     []string{"dws deap +employee-publish --assistant <assistantId>"},
		},
	},
	Flags: []shortcut.Flag{
		{Name: "assistant", Type: shortcut.FlagString, Desc: "数字员工 ID (assistantId)", Required: true},
		{Name: "no-join-group", Type: shortcut.FlagBool, Desc: "禁止被拉入群聊 (默认允许)"},
	},
	Tips: []string{"dws deap +employee-publish --assistant <assistantId>"},
	Execute: func(rt *shortcut.RuntimeContext) error {
		// 服务端缺省允许入群；这里用反向开关表达，避免布尔标志"不传即 false"
		// 与服务端"不传即 true"的缺省相互矛盾
		return rt.CallMCP("publish_digital_employee", map[string]any{
			"agentUuid":      rt.Str("assistant"),
			"allowJoinGroup": !rt.Bool("no-join-group"),
		})
	},
}

// EmployeeDelete removes a digital employee. Irreversible.
var EmployeeDelete = shortcut.Shortcut{
	Service: "deap", Command: "+employee-delete", Product: "deap",
	Description: "删除数字员工（不可逆）",
	Intent:      "当你确认要彻底移除某个数字员工时使用。这是不可逆操作：服务端会撤销运行态感知、删除本体、清理触发规则。且事务只覆盖数据库，对外的推送与消息副作用不回滚——若返回失败，不要简单重试，应先用 +employee-detail 确认它当前是否还存在。",
	Risk:        shortcut.RiskWrite,
	Safety:      destructiveSafety(),
	Contract: corecmd.ContractDecl{
		Identity:    identity("+employee-delete"),
		Description: "删除数字员工（不可逆）",
		Interface:   compositeInterface(),
		Selection: contract.SelectionSpec{
			AgentSummary: "删除数字员工（不可逆）",
			UseWhen:      []string{"当你确认要彻底移除某个数字员工时使用；不可逆，请先确认 ID 无误。"},
			AvoidWhen:    []string{"只想让它暂时不响应时应改配置而非删除；删除无法撤销"},
			Examples:     []string{"dws deap +employee-delete --assistant <assistantId>"},
		},
	},
	Flags: []shortcut.Flag{
		{Name: "assistant", Type: shortcut.FlagString, Desc: "数字员工 ID (assistantId)", Required: true},
	},
	Tips: []string{"dws deap +employee-delete --assistant <assistantId>"},
	Execute: func(rt *shortcut.RuntimeContext) error {
		return rt.CallMCP("delete_digital_employee", map[string]any{
			"agentUuid": rt.Str("assistant"),
		})
	},
}

// buildProfile assembles the employee profile sub-object shared by create and
// save-draft. Returns an empty map when the caller supplied none of the fields, so
// the key is omitted rather than sent as an empty object — under the draft's
// overwrite semantics an empty object would wipe the stored profile.
func buildProfile(rt *shortcut.RuntimeContext) map[string]any {
	profile := map[string]any{}
	for flag, key := range map[string]string{
		"employee-no":    "employeeNo",
		"position-name":  "positionName",
		"supervisor-uid": "directSupervisorUid",
		"response-mode":  "responseMode",
	} {
		if v := rt.Str(flag); v != "" {
			profile[key] = v
		}
	}
	return profile
}

func init() {
	shortcut.Register(
		EmployeeList,
		EmployeeDetail,
		EmployeeCreate,
		EmployeeSaveDraft,
		EmployeePublish,
		EmployeeDelete,
	)
}
