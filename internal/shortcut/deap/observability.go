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

// Observability shortcuts for DEAP executions (数字员工的一次执行).
//
// Three things about this surface are easy to get wrong, so they are stated here
// once and repeated in each command's Intent:
//
//  1. 状态查询按场域分两个口径。单聊走 DEAP 既有链路，服务端生成 taskId 并登记状态，
//     可直接用 +run-status 轮询；群内走 Runtime V3 入口，taskId 是调用方传进去的、
//     服务端不登记，只能用消息 ID 经 +run-executions 现查。传错口径会稳定查不到。
//  2. 反查用的是 openMessageId，不是 openTaskId。openTaskId 是发消息接口返回的发送句柄，
//     openMessageId 是消息投递后 IM 才生成的业务键。手上只有 openTaskId 时，
//     需先用 `dws chat message query-send-status` 换取 openMessageId。
//  3. trace 含完整对话内容，权限比状态更严。服务端先做两级权限校验再取内容：
//     管理者可看该数字员工全部执行，使用者仅可看自己触发的。
package deap

import (
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/corecmd"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/corecmd/contract"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/shortcut"
)

// RunStatus polls one execution by taskId (单聊口径).
var RunStatus = shortcut.Shortcut{
	Service: "deap", Command: "+run-status", Product: "deap",
	Description: "查询数字员工单次执行状态（单聊口径，按 taskId）",
	Intent:      "当你在单聊中触发了数字员工、想知道这次执行跑到哪一步（运行中 / 成功 / 失败 / 已中止）时使用。仅适用于单聊：单聊由服务端生成 taskId 并登记状态所以可轮询；群内触发的执行服务端不登记 taskId，要改用 +run-executions 按消息 ID 查，否则会稳定查不到。只读操作。",
	Risk:        shortcut.RiskRead,
	Safety:      readSafety(),
	Contract: corecmd.ContractDecl{
		Identity:    identity("+run-status"),
		Description: "查询数字员工单次执行状态（单聊口径）",
		Interface:   compositeInterface(),
		Selection: contract.SelectionSpec{
			AgentSummary: "查询数字员工单次执行状态（单聊口径）",
			UseWhen:      []string{"当你要查单聊中某次执行的状态、且手上有服务端返回的 taskId 时使用。"},
			AvoidWhen:    []string{"群内触发的执行没有可查的 taskId，改用 +run-executions 按消息 ID 查"},
			Examples:     []string{"dws deap +run-status --assistant <assistantId> --task <taskId>"},
		},
	},
	Flags: []shortcut.Flag{
		{Name: "assistant", Type: shortcut.FlagString, Desc: "数字员工 ID (assistantId)", Required: true},
		{Name: "task", Type: shortcut.FlagString, Desc: "任务 ID (taskId，由服务端生成)", Required: true},
	},
	Tips: []string{"dws deap +run-status --assistant <assistantId> --task <taskId>"},
	Execute: func(rt *shortcut.RuntimeContext) error {
		return rt.CallMCP("query_digital_employee_run_status", map[string]any{
			"assistantId": rt.Str("assistant"),
			"taskId":      rt.Str("task"),
		})
	},
}

// RunExecutions batch-queries executions by user message id (群内口径).
var RunExecutions = shortcut.Shortcut{
	Service: "deap", Command: "+run-executions", Product: "deap",
	Description: "批量查询数字员工执行记录（群内口径，按用户消息 ID）",
	Intent:      "当你要查群内触发的数字员工执行记录（状态、触发来源、触发规则）时使用，这是群内场域唯一可查的口径。传的必须是发起本次执行的用户消息 ID，不能传助手消息 ID——触发源只写在用户消息上，传错会查到一条缺触发源的记录。查不到的消息 ID 不会出现在结果里，需自行判断缺失。只读操作。",
	Risk:        shortcut.RiskRead,
	Safety:      readSafety(),
	Contract: corecmd.ContractDecl{
		Identity:    identity("+run-executions"),
		Description: "批量查询数字员工执行记录（群内口径）",
		Interface:   compositeInterface(),
		Selection: contract.SelectionSpec{
			AgentSummary: "批量查询数字员工执行记录（群内口径）",
			UseWhen:      []string{"当你要查群内触发的执行状态、或一次查多条执行记录时使用；传用户消息 ID。"},
			AvoidWhen:    []string{"单聊场景手上有 taskId 时用 +run-status 更直接"},
			Examples:     []string{"dws deap +run-executions --messages <messageId1>,<messageId2>"},
		},
	},
	Flags: []shortcut.Flag{
		{Name: "messages", Type: shortcut.FlagStringSlice, Desc: "用户消息 ID 列表（发起执行的那条用户消息，非助手消息）", Required: true},
	},
	Tips: []string{"dws deap +run-executions --messages <messageId1>,<messageId2>"},
	Execute: func(rt *shortcut.RuntimeContext) error {
		return rt.CallMCP("query_digital_employee_run_executions", map[string]any{
			"messageIds": rt.StrSlice("messages"),
		})
	},
}

// RunResolve maps an external source id back to the execution runId.
var RunResolve = shortcut.Shortcut{
	Service: "deap", Command: "+run-resolve", Product: "deap",
	Description: "按来源 ID 反查执行 runId",
	Intent:      "当你手上只有钉钉消息 ID（openMessageId）、想找到它对应的那次执行以便进一步拉 trace 时使用。注意传的是 openMessageId 而不是 openTaskId：后者是发消息接口返回的发送句柄，需先用 `dws chat message query-send-status --open-task-id <openTaskId>` 换成 openMessageId。未建立映射时返回空而不报错——无法从 ID 形态预判是否存在映射。只读操作。",
	Risk:        shortcut.RiskRead,
	Safety:      readSafety(),
	Contract: corecmd.ContractDecl{
		Identity:    identity("+run-resolve"),
		Description: "按来源 ID 反查执行 runId",
		Interface:   compositeInterface(),
		Selection: contract.SelectionSpec{
			AgentSummary: "按来源 ID 反查执行 runId",
			UseWhen:      []string{"当你只有钉钉消息 ID、要找到对应执行的 runId 以便拉 trace 时使用。"},
			AvoidWhen:    []string{"手上是 openTaskId 时先用 chat 的发送状态查询换成 openMessageId，本命令不接受 openTaskId"},
			Examples:     []string{"dws deap +run-resolve --source <openMessageId>"},
		},
	},
	Flags: []shortcut.Flag{
		{Name: "source", Type: shortcut.FlagString, Desc: "来源侧原始 ID（钉钉场域填 openMessageId）", Required: true},
		{Name: "source-type", Type: shortcut.FlagString,
			Enum: []string{"im_message", "trigger_rule", "scenario_instance"},
			Desc: "来源类型 (可选，默认 im_message)"},
	},
	Tips: []string{
		"dws deap +run-resolve --source <openMessageId>",
		"# 手上只有 openTaskId 时先换 openMessageId：",
		"dws chat message query-send-status --open-task-id <openTaskId>",
	},
	Execute: func(rt *shortcut.RuntimeContext) error {
		params := map[string]any{"sourceId": rt.Str("source")}
		// 不传时由服务端按 im_message 解读；这里不代填，避免把默认值固化在两处
		if v := rt.Str("source-type"); v != "" {
			params["sourceType"] = v
		}
		return rt.CallMCP("resolve_digital_employee_run", params)
	},
}

// RunTrace pulls the full Langfuse trace for one execution.
var RunTrace = shortcut.Shortcut{
	Service: "deap", Command: "+run-trace", Product: "deap",
	Description: "拉取一次执行的完整 trace",
	Intent:      "当你要排查数字员工某次执行的详细过程（模型调用、工具调用、中间结果）时使用，返回该次执行的完整 trace 原始内容。trace 含完整对话内容，权限比状态查询更严：管理者可看该数字员工全部执行，使用者仅可看自己触发的，无权时直接拒绝且不会读取内容。traceId 可从 +run-resolve 或执行记录中获得，30 位与 32 位两种形态都接受。只读操作。",
	Risk:        shortcut.RiskRead,
	Safety:      readSafety(),
	Contract: corecmd.ContractDecl{
		Identity:    identity("+run-trace"),
		Description: "拉取一次执行的完整 trace",
		Interface:   compositeInterface(),
		Selection: contract.SelectionSpec{
			AgentSummary: "拉取一次执行的完整 trace",
			UseWhen:      []string{"当你要排查某次执行的详细过程、看模型与工具调用链路时使用。"},
			AvoidWhen:    []string{"只想知道成功/失败时用 +run-status 或 +run-executions，trace 数据量大且更敏感"},
			Examples:     []string{"dws deap +run-trace --trace <traceId>"},
		},
	},
	Flags: []shortcut.Flag{
		{Name: "trace", Type: shortcut.FlagString, Desc: "执行的 traceId（即 runId），30 位或 32 位形态均可", Required: true},
	},
	Tips: []string{
		"dws deap +run-trace --trace <traceId>",
		"# 只有钉钉消息 ID 时先反查 runId：",
		"dws deap +run-resolve --source <openMessageId>",
	},
	Execute: func(rt *shortcut.RuntimeContext) error {
		return rt.CallMCP("query_digital_employee_trace", map[string]any{
			"traceId": rt.Str("trace"),
		})
	},
}

func init() {
	shortcut.Register(
		RunStatus,
		RunExecutions,
		RunResolve,
		RunTrace,
	)
}
