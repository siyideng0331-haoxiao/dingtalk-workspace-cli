// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package helpers

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/corecmd/contract"
	apperrors "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/errors"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/pkg/cmdutil"
	"github.com/spf13/cobra"
)

const (
	deapAgentServerID = "deap-dev"

	deapAgentCreateTool     = "create_digital_employee"
	deapAgentDetailTool     = "get_digital_employee_detail"
	deapAgentListTool       = "list_digital_employees"
	deapAgentSaveDraftTool  = "update_digital_employee_draft"
	deapAgentPublishTool    = "publish_digital_employee"
	deapAgentDeleteTool     = "delete_digital_employee"
	deapAgentRunStatusTool  = "query_de_run_status"
	deapAgentExecutionsTool = "query_de_run_executions"
	deapAgentResolveRunTool = "resolve_de_run_id"
	deapAgentTraceTool      = "query_de_trace"
)

var deapAgentDryRun = &contract.DryRunSpec{
	PreviewKind: contract.DryRunPreviewInvocation,
	RemoteReads: false,
}

func newDeapAgentCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "deap-agent",
		Short:             "管理和观测 DEAP 数字员工",
		Long:              "DEAP 数字员工低频开发者命令组。固定调用 MCP product/server deap-dev；identity.corpId/userId 由可信登录态注入且不对 CLI 暴露。端点需通过 DINGTALK_DEAP_DEV_MCP_URL 显式配置。",
		Args:              cobra.NoArgs,
		TraverseChildren:  true,
		DisableAutoGenTag: true,
		RunE:              groupRunE,
	}
	cmdutil.MarkGroup(cmd)
	cmd.AddCommand(
		newDeapAgentCreateCommand(),
		newDeapAgentDetailCommand(),
		newDeapAgentListCommand(),
		newDeapAgentSaveDraftCommand(),
		newDeapAgentPublishCommand(),
		newDeapAgentDeleteCommand(),
		newDeapAgentRunStatusCommand(),
		newDeapAgentRunExecutionsCommand(),
		newDeapAgentResolveRunIDCommand(),
		newDeapAgentTraceCommand(),
	)
	return cmd
}

func newDeapAgentCreateCommand() *cobra.Command {
	return NewLeafCommand(LeafSpec{
		Use:   "create",
		Short: "创建草稿态数字员工",
		Long:  "创建草稿态数字员工并返回 assistantId；不会自动发布。名称、简介和归属组织必填，identity 由 MCP 可信注入。档案字段可用独立 flag 或 profile-json 提供；同时提供时独立 flag 覆盖 JSON 同名字段。创建后需补齐头像、岗位、响应模式和人设提示词，再保存草稿并发布。",
		Tool:  deapAgentCreateTool,
		Server: deapAgentServerID,
		PostMount: deapAgentNoArgs,
		Flags: []LeafFlag{
			{Name: "name", Usage: "数字员工名称（最多 30 个 Unicode 码点）", Bind: "name", Required: true, Trim: true},
			{Name: "description", Usage: "数字员工简介（最多 300 个 Unicode 码点）", Bind: "description", Required: true, Trim: true},
			{Name: "org-code", Usage: "归属部门编码", Bind: "orgCode", Required: true, Trim: true},
			{Name: "org-name", Usage: "归属部门名称（服务端会按 ACL 重解析）", Bind: "orgName", Required: true, Trim: true},
			{Name: "icon", Usage: "头像地址或 OSS objectPath", Bind: "icon", Trim: true, OmitEmpty: true},
			{Name: "profile-json", Usage: "digitalTagEmployeeProfile JSON 对象；独立档案 flag 会覆盖同名字段", Bind: "digitalTagEmployeeProfile", Trim: true, OmitEmpty: true, Format: "json", Transform: deapAgentProfileJSON, SchemaDescription: "数字员工档案 JSON；仅接收 employeeNo、positionName、directSupervisorUid、responseMode；独立档案参数优先"},
			{Name: "employee-no", Usage: "数字员工工号（最多 64 个 Unicode 码点）", Bind: "digitalTagEmployeeProfile.employeeNo", Trim: true, OmitEmpty: true},
			{Name: "position-name", Usage: "岗位名称（最多 128 个 Unicode 码点，发布前必填）", Bind: "digitalTagEmployeeProfile.positionName", Trim: true, OmitEmpty: true},
			{Name: "supervisor-uid", Usage: "直属上级钉钉 uid", Bind: "digitalTagEmployeeProfile.directSupervisorUid", Trim: true, OmitEmpty: true},
			{Name: "response-mode", Usage: "响应模式：mention_only 或 targeted_proactive（发布前必填）", Bind: "digitalTagEmployeeProfile.responseMode", Trim: true, OmitEmpty: true, Enum: []string{"mention_only", "targeted_proactive"}},
		},
		Safety: contract.SafetySpec{
			Effect: "write", Risk: "medium",
			Confirmation: "not_required", Idempotency: "non_idempotent",
		},
		Validate: func(cmd *cobra.Command, args []string) error {
			if err := deapAgentMaxRunes(cmd, "name", 30); err != nil {
				return err
			}
			if err := deapAgentMaxRunes(cmd, "description", 300); err != nil {
				return err
			}
			if err := deapAgentMaxRunes(cmd, "employee-no", 64); err != nil {
				return err
			}
			return deapAgentMaxRunes(cmd, "position-name", 128)
		},
		Call: deapAgentCallWithProfile,
		Contract: LeafContract{
			Identity: contract.ToolIdentitySpec{
				ProductID: "dev", Name: "create_digital_employee",
				CanonicalPath: "dev.create_digital_employee",
				CLIPath: "dev deap-agent create", PrimaryCLIPath: "dev deap-agent create",
				Group: "deap-agent",
			},
			Description: "创建草稿态 DEAP 数字员工并返回 assistantId；不会自动发布。",
			DryRun: deapAgentDryRun,
			Interface: deapAgentMCPInterface(deapAgentCreateTool),
			Selection: contract.SelectionSpec{
				AgentSummary: "创建新的草稿态 DEAP 数字员工",
				UseWhen: []string{"需要从零创建数字员工并获得 assistantId 时"},
				AvoidWhen: []string{"已有 agentUuid 只需修改草稿时使用 save-draft", "创建普通开放平台应用时使用 dev app create"},
				Examples: []string{`dws dev deap-agent create --name "值班助手" --description "处理值班问题" --org-code dept-1 --org-name "值班组" --position-name "值班员" --response-mode mention_only --dry-run --format json`},
			},
			Parameters: []contract.ParamDecl{
				{Name: "profile-json", Property: "digitalTagEmployeeProfile", InterfaceType: "object"},
				{Name: "employee-no", Property: "digitalTagEmployeeProfile.employeeNo"},
				{Name: "position-name", Property: "digitalTagEmployeeProfile.positionName"},
				{Name: "supervisor-uid", Property: "digitalTagEmployeeProfile.directSupervisorUid"},
				{Name: "response-mode", Property: "digitalTagEmployeeProfile.responseMode"},
			},
		},
	})
}

func newDeapAgentDetailCommand() *cobra.Command {
	return NewLeafCommand(LeafSpec{
		Use:   "detail",
		Short: "查询数字员工管理态详情",
		Long:  "查询数字员工管理态详情，包含草稿回写和发布校验所需字段。iconUrl 可能是带签名的临时地址，长期保存应使用 icon。",
		Tool:  deapAgentDetailTool,
		Server: deapAgentServerID,
		PostMount: deapAgentNoArgs,
		Flags: []LeafFlag{
			{Name: "assistant-id", Usage: "数字员工 ID", Bind: "assistantId", Required: true, Trim: true},
		},
		Safety: contract.SafetySpec{
			Effect: "read", Risk: "low",
			Confirmation: "not_required", Idempotency: "idempotent",
		},
		Contract: LeafContract{
			Identity: contract.ToolIdentitySpec{
				ProductID: "dev", Name: "get_digital_employee_detail",
				CanonicalPath: "dev.get_digital_employee_detail",
				CLIPath: "dev deap-agent detail", PrimaryCLIPath: "dev deap-agent detail",
				Group: "deap-agent",
			},
			Description: "查询指定 DEAP 数字员工的管理态详情。",
			DryRun: deapAgentDryRun,
			Interface: deapAgentMCPInterface(deapAgentDetailTool),
			Selection: contract.SelectionSpec{
				AgentSummary: "按 assistantId 查询数字员工管理态详情",
				UseWhen: []string{"需要读取数字员工完整配置、发布前检查或保存草稿前回读时"},
				AvoidWhen: []string{"需要分页查找多个数字员工时使用 list", "只查一次运行状态时使用 run-status"},
				Examples: []string{"dws dev deap-agent detail --assistant-id <assistantId> --format json"},
			},
		},
	})
}

func newDeapAgentListCommand() *cobra.Command {
	return NewLeafCommand(LeafSpec{
		Use:   "list",
		Short: "分页查询数字员工",
		Long:  "分页查询当前身份有权查看的数字员工。管理员可看本组织全部，非管理员只返回本人参与的数字员工；支持按名称、岗位或工号模糊搜索。page 和 page-size 必须大于等于 1。",
		Tool:  deapAgentListTool,
		Server: deapAgentServerID,
		PostMount: deapAgentNoArgs,
		Flags: []LeafFlag{
			{Name: "keyword", Usage: "按名称、岗位或工号模糊匹配", Bind: "keyword", Trim: true, OmitEmpty: true},
			{Name: "page", Usage: "页码", Bind: "page", Kind: LeafInt, Default: "1", ArgDefault: "1"},
			{Name: "page-size", Usage: "每页数量", Bind: "pageSize", Kind: LeafInt, Default: "20", ArgDefault: "20"},
		},
		Safety: contract.SafetySpec{
			Effect: "read", Risk: "low",
			Confirmation: "not_required", Idempotency: "idempotent",
		},
		Validate: func(cmd *cobra.Command, args []string) error {
			page, _ := cmd.Flags().GetInt("page")
			if page < 1 {
				return apperrors.NewValidation("参数 --page 不能小于 1")
			}
			pageSize, _ := cmd.Flags().GetInt("page-size")
			if pageSize < 1 {
				return apperrors.NewValidation("参数 --page-size 不能小于 1")
			}
			return nil
		},
		Contract: LeafContract{
			Identity: contract.ToolIdentitySpec{
				ProductID: "dev", Name: "list_digital_employees",
				CanonicalPath: "dev.list_digital_employees",
				CLIPath: "dev deap-agent list", PrimaryCLIPath: "dev deap-agent list",
				Group: "deap-agent",
			},
			Description: "分页查询当前身份有权查看的 DEAP 数字员工。",
			DryRun: deapAgentDryRun,
			Interface: deapAgentMCPInterface(deapAgentListTool),
			Selection: contract.SelectionSpec{
				AgentSummary: "分页查找当前用户可管理或参与的数字员工",
				UseWhen: []string{"需要按名称、岗位或工号查找数字员工，或尚不知道 agentUuid 时"},
				AvoidWhen: []string{"已知 agentUuid 需要完整配置时使用 detail", "需要查询运行记录时使用 run-status"},
				Examples: []string{`dws dev deap-agent list --keyword "值班" --page 1 --page-size 20 --format json`},
			},
		},
	})
}

func newDeapAgentSaveDraftCommand() *cobra.Command {
	return NewLeafCommand(LeafSpec{
		Use:   "save-draft",
		Short: "全量覆写数字员工草稿",
		Long:  "高风险全量覆写：save-draft 会整体替换 draft JSON，未传字段会被清空。档案字段可用独立 flag 或 profile-json 提供；同时提供时独立 flag 覆盖 JSON 同名字段。增量修改必须先执行 detail，保留全部需要的字段后再整体回写；请先 --dry-run 检查参数，再经用户确认加 --yes。",
		Tool:  deapAgentSaveDraftTool,
		Server: deapAgentServerID,
		PostMount: deapAgentNoArgs,
		Flags: []LeafFlag{
			{Name: "agent-uuid", Usage: "数字员工 ID", Bind: "agentUuid", Required: true, Trim: true},
			{Name: "name", Usage: "数字员工名称（最多 30 个 Unicode 码点）", Bind: "name", Trim: true, OmitEmpty: true},
			{Name: "description", Usage: "数字员工简介（最多 300 个 Unicode 码点）", Bind: "description", Trim: true, OmitEmpty: true},
			{Name: "icon", Usage: "头像 OSS objectPath；不要持久化详情中的临时 iconUrl", Bind: "icon", Trim: true, OmitEmpty: true},
			{Name: "org-code", Usage: "归属部门编码", Bind: "orgCode", Trim: true, OmitEmpty: true},
			{Name: "org-name", Usage: "归属部门名称", Bind: "orgName", Trim: true, OmitEmpty: true},
			{Name: "prompt", Usage: "人设/System Prompt（最多 5000 个 Unicode 码点）", Bind: "prompt", Trim: true, OmitEmpty: true},
			{Name: "profile-json", Usage: "digitalTagEmployeeProfile JSON 对象；独立档案 flag 会覆盖同名字段", Bind: "digitalTagEmployeeProfile", Trim: true, OmitEmpty: true, Format: "json", Transform: deapAgentProfileJSON},
			{Name: "employee-no", Usage: "数字员工工号（最多 64 个 Unicode 码点）", Bind: "digitalTagEmployeeProfile.employeeNo", Trim: true, OmitEmpty: true},
			{Name: "position-name", Usage: "岗位名称（最多 128 个 Unicode 码点，发布前必填）", Bind: "digitalTagEmployeeProfile.positionName", Trim: true, OmitEmpty: true},
			{Name: "supervisor-uid", Usage: "直属上级钉钉 uid", Bind: "digitalTagEmployeeProfile.directSupervisorUid", Trim: true, OmitEmpty: true},
			{Name: "response-mode", Usage: "响应模式：mention_only 或 targeted_proactive（发布前必填）", Bind: "digitalTagEmployeeProfile.responseMode", Trim: true, OmitEmpty: true, Enum: []string{"mention_only", "targeted_proactive"}},
		},
		Safety: contract.SafetySpec{
			Effect: "write", Risk: "high",
			Confirmation: "user_required", Idempotency: "idempotent",
		},
		Validate: func(cmd *cobra.Command, args []string) error {
			if err := deapAgentMaxRunes(cmd, "name", 30); err != nil {
				return err
			}
			if err := deapAgentMaxRunes(cmd, "description", 300); err != nil {
				return err
			}
			if err := deapAgentMaxRunes(cmd, "prompt", 5000); err != nil {
				return err
			}
			if err := deapAgentMaxRunes(cmd, "employee-no", 64); err != nil {
				return err
			}
			return deapAgentMaxRunes(cmd, "position-name", 128)
		},
		Call: deapAgentCallWithProfile,
		Contract: LeafContract{
			Identity: contract.ToolIdentitySpec{
				ProductID: "dev", Name: "update_digital_employee_draft",
				CanonicalPath: "dev.update_digital_employee_draft",
				CLIPath: "dev deap-agent save-draft", PrimaryCLIPath: "dev deap-agent save-draft",
				Group: "deap-agent",
			},
			Description: "全量覆写指定 DEAP 数字员工的草稿；未传字段会被清空。",
			DryRun: deapAgentDryRun,
			Interface: deapAgentMCPInterface(deapAgentSaveDraftTool),
			Selection: contract.SelectionSpec{
				AgentSummary: "全量覆写数字员工草稿配置",
				UseWhen: []string{"已先读取完整详情、明确保留字段，并需要保存尚未发布的数字员工草稿时"},
				AvoidWhen: []string{"只改单字段但尚未 detail 回读全量时不要执行", "准备直接上线时仍需另行执行 publish"},
				Examples: []string{`dws dev deap-agent save-draft --agent-uuid <agentUuid> --name "值班助手" --description "处理值班问题" --dry-run --format json`},
			},
			Parameters: []contract.ParamDecl{
				{Name: "profile-json", Property: "digitalTagEmployeeProfile", InterfaceType: "object"},
				{Name: "employee-no", Property: "digitalTagEmployeeProfile.employeeNo"},
				{Name: "position-name", Property: "digitalTagEmployeeProfile.positionName"},
				{Name: "supervisor-uid", Property: "digitalTagEmployeeProfile.directSupervisorUid"},
				{Name: "response-mode", Property: "digitalTagEmployeeProfile.responseMode"},
			},
		},
	})
}

func newDeapAgentPublishCommand() *cobra.Command {
	return NewLeafCommand(LeafSpec{
		Use:   "publish",
		Short: "发布数字员工",
		Long:  "发布数字员工。服务端会对名称、头像、简介、部门、岗位、响应模式和 System Prompt 执行完整校验，并下发到钉钉；这是高影响操作，真实执行前必须确认。",
		Tool:  deapAgentPublishTool,
		Server: deapAgentServerID,
		PostMount: deapAgentNoArgs,
		Flags: []LeafFlag{
			{Name: "agent-uuid", Usage: "数字员工 ID", Bind: "agentUuid", Required: true, Trim: true},
			{Name: "allow-join-group", Usage: "是否允许被拉入群（未显式传递时服务端默认 true）", Bind: "allowJoinGroup", Kind: LeafBool, Default: "true"},
		},
		Safety: contract.SafetySpec{
			Effect: "write", Risk: "high",
			Confirmation: "user_required", Idempotency: "unknown",
		},
		Contract: LeafContract{
			Identity: contract.ToolIdentitySpec{
				ProductID: "dev", Name: "publish_digital_employee",
				CanonicalPath: "dev.publish_digital_employee",
				CLIPath: "dev deap-agent publish", PrimaryCLIPath: "dev deap-agent publish",
				Group: "deap-agent",
			},
			Description: "校验并发布指定 DEAP 数字员工。",
			DryRun: deapAgentDryRun,
			Interface: deapAgentMCPInterface(deapAgentPublishTool),
			Selection: contract.SelectionSpec{
				AgentSummary: "校验并发布数字员工，使草稿配置进入线上生效流程",
				UseWhen: []string{"草稿配置已完成并经用户明确确认，需要发布到钉钉时"},
				AvoidWhen: []string{"只需保存未发布草稿时使用 save-draft", "发布必填配置不完整时先 detail 检查并补齐"},
				Examples: []string{"dws dev deap-agent publish --agent-uuid <agentUuid> --dry-run --format json"},
			},
		},
	})
}

func newDeapAgentDeleteCommand() *cobra.Command {
	return NewLeafCommand(LeafSpec{
		Use:   "delete",
		Short: "删除数字员工",
		Long:  "删除指定数字员工。数据库事务不覆盖全部 RPC/MQ 副作用，删除后外部副作用可能无法回滚；这是不可逆高风险操作，真实执行前必须确认。",
		Tool:  deapAgentDeleteTool,
		Server: deapAgentServerID,
		PostMount: deapAgentNoArgs,
		Flags: []LeafFlag{
			{Name: "agent-uuid", Usage: "数字员工 ID", Bind: "agentUuid", Required: true, Trim: true},
		},
		Safety: contract.SafetySpec{
			Effect: "destructive", Risk: "high",
			Confirmation: "user_required", Idempotency: "unknown",
		},
		Contract: LeafContract{
			Identity: contract.ToolIdentitySpec{
				ProductID: "dev", Name: "delete_digital_employee",
				CanonicalPath: "dev.delete_digital_employee",
				CLIPath: "dev deap-agent delete", PrimaryCLIPath: "dev deap-agent delete",
				Group: "deap-agent",
			},
			Description: "删除指定 DEAP 数字员工；外部副作用可能无法回滚。",
			DryRun: deapAgentDryRun,
			Interface: deapAgentMCPInterface(deapAgentDeleteTool),
			Selection: contract.SelectionSpec{
				AgentSummary: "永久删除指定数字员工",
				UseWhen: []string{"用户明确要求删除数字员工，并已确认 agentUuid 与不可逆影响时"},
				AvoidWhen: []string{"只需停止发布或暂时修改配置时不要删除", "未确认目标与影响范围时不要执行"},
				Examples: []string{"dws dev deap-agent delete --agent-uuid <agentUuid> --dry-run --format json"},
			},
		},
	})
}

func newDeapAgentRunStatusCommand() *cobra.Command {
	return NewLeafCommand(LeafSpec{
		Use:   "run-status",
		Short: "查询单聊数字员工运行状态",
		Long:  "按 assistantId 和服务端生成的 taskId 查询单聊执行状态。只适用于通过 DEAP 单聊执行入口发起并登记 taskId 的运行；群内运行请使用 run-executions。",
		Tool:  deapAgentRunStatusTool,
		Server: deapAgentServerID,
		PostMount: deapAgentNoArgs,
		Flags: []LeafFlag{
			{Name: "assistant-id", Usage: "智能体/数字员工 ID", Bind: "assistantId", Required: true, Trim: true},
			{Name: "task-id", Usage: "单聊发起接口返回、由服务端登记的任务 ID", Bind: "taskId", Required: true, Trim: true},
		},
		Safety: contract.SafetySpec{
			Effect: "read", Risk: "low",
			Confirmation: "not_required", Idempotency: "idempotent",
		},
		Contract: LeafContract{
			Identity: contract.ToolIdentitySpec{
				ProductID: "dev", Name: "query_de_run_status",
				CanonicalPath: "dev.query_de_run_status",
				CLIPath: "dev deap-agent run-status", PrimaryCLIPath: "dev deap-agent run-status",
				Group: "deap-agent",
			},
			Description: "按 assistantId 和 taskId 查询 DEAP 单聊运行状态。",
			DryRun: deapAgentDryRun,
			Interface: deapAgentMCPInterface(deapAgentRunStatusTool),
			Selection: contract.SelectionSpec{
				AgentSummary: "按 assistantId 和 taskId 查询数字员工单聊运行状态",
				UseWhen: []string{"已有单聊发起接口返回的 taskId 与对应 assistantId，需要判断运行状态时"},
				AvoidWhen: []string{"群消息运行使用 run-executions", "需要查看完整 Langfuse 链路时使用 trace"},
				Examples: []string{"dws dev deap-agent run-status --assistant-id <assistantId> --task-id <taskId> --format json"},
			},
		},
	})
}

func newDeapAgentRunExecutionsCommand() *cobra.Command {
	return NewLeafCommand(LeafSpec{
		Use:   "run-executions",
		Short: "批量查询群消息数字员工执行结果",
		Long:  "按 chat-2 用户消息 ID 批量查询数字员工在群消息感知、群聊或定时任务场域中的执行结果。必须传用户消息 ID，不要传助手消息 ID；查不到的 ID 不会出现在返回 Map 中。",
		Tool:  deapAgentExecutionsTool,
		Server: deapAgentServerID,
		PostMount: deapAgentNoArgs,
		Flags: []LeafFlag{
			{Name: "message-ids-json", Usage: "chat-2 用户消息 ID 的 JSON 数组", Bind: "messageIds", Required: true, Trim: true, Format: "json", Transform: deapAgentMessageIDsJSON, SchemaDescription: "发起执行的 chat-2 用户消息 ID 列表"},
		},
		Safety: contract.SafetySpec{
			Effect: "read", Risk: "low",
			Confirmation: "not_required", Idempotency: "idempotent",
		},
		Contract: LeafContract{
			Identity: contract.ToolIdentitySpec{
				ProductID: "dev", Name: "query_de_run_executions",
				CanonicalPath: "dev.query_de_run_executions",
				CLIPath: "dev deap-agent run-executions", PrimaryCLIPath: "dev deap-agent run-executions",
				Group: "deap-agent",
			},
			Description: "按 chat-2 用户消息 ID 批量查询 DEAP 执行结果。",
			DryRun: deapAgentDryRun,
			Interface: deapAgentMCPInterface(deapAgentExecutionsTool),
			Selection: contract.SelectionSpec{
				AgentSummary: "按用户消息 ID 批量查询群聊等场域的数字员工执行结果",
				UseWhen: []string{"持有一个或多个 chat-2 用户消息 ID，需要查询群消息感知、群聊或定时任务执行结果时"},
				AvoidWhen: []string{"不要传助手消息 ID", "单聊 taskId 状态查询使用 run-status"},
				Examples: []string{`dws dev deap-agent run-executions --message-ids-json '["<openMessageId>"]' --format json`},
			},
			Parameters: []contract.ParamDecl{
				{Name: "message-ids-json", Property: "messageIds", InterfaceType: "array"},
			},
		},
	})
}

func newDeapAgentResolveRunIDCommand() *cobra.Command {
	return NewLeafCommand(LeafSpec{
		Use:   "resolve-run-id",
		Short: "按开放态消息 ID 反查 runId",
		Long:  "按钉钉开放态消息 ID（openMessageId）反查本次执行的 runId 和 Langfuse traceId。DWS openTaskId 不是 openMessageId，必须先通过发送状态查询换成 openMessageId。sourceType 固定为 im_message。",
		Tool:  deapAgentResolveRunTool,
		Server: deapAgentServerID,
		PostMount: deapAgentNoArgs,
		Flags: []LeafFlag{
			{Name: "source-id", Usage: "钉钉 IM 投递后的 openMessageId；不是 DWS openTaskId", Bind: "sourceId", Required: true, Trim: true},
		},
		Safety: contract.SafetySpec{
			Effect: "read", Risk: "low",
			Confirmation: "not_required", Idempotency: "idempotent",
		},
		ConstParams: map[string]any{"sourceType": "im_message"},
		Contract: LeafContract{
			Identity: contract.ToolIdentitySpec{
				ProductID: "dev", Name: "resolve_de_run_id",
				CanonicalPath: "dev.resolve_de_run_id",
				CLIPath: "dev deap-agent resolve-run-id", PrimaryCLIPath: "dev deap-agent resolve-run-id",
				Group: "deap-agent",
			},
			Description: "按 openMessageId 反查 DEAP runId 和 Langfuse traceId。",
			DryRun: deapAgentDryRun,
			Interface: deapAgentMCPInterface(deapAgentResolveRunTool),
			Selection: contract.SelectionSpec{
				AgentSummary: "按 openMessageId 反查 DEAP runId 与 Langfuse traceId",
				UseWhen: []string{"持有钉钉开放态 openMessageId，需要定位对应 DEAP runId 或 traceId 时"},
				AvoidWhen: []string{"手上只有 DWS openTaskId 时先查询发送状态获取 openMessageId", "不要把 openTaskId 冒充 openMessageId"},
				Examples: []string{"dws dev deap-agent resolve-run-id --source-id <openMessageId> --format json"},
			},
		},
	})
}

func newDeapAgentTraceCommand() *cobra.Command {
	return NewLeafCommand(LeafSpec{
		Use:   "trace",
		Short: "查询数字员工 Langfuse 链路",
		Long:  "按 EagleEye runId 或 Langfuse traceId 查询完整 Langfuse 链路。返回内容可能包含完整对话和模型输入输出；服务端会先对调用人执行两级授权，无权时不会读取 trace 内容。返回 data 是 Langfuse 原始 JSON 字符串。",
		Tool:  deapAgentTraceTool,
		Server: deapAgentServerID,
		PostMount: deapAgentNoArgs,
		Flags: []LeafFlag{
			{Name: "trace-id", Usage: "30 位 EagleEye runId 或 32 位 Langfuse traceId", Bind: "traceId", Required: true, Trim: true},
		},
		Safety: contract.SafetySpec{
			Effect: "read", Risk: "high",
			Confirmation: "not_required", Idempotency: "idempotent",
		},
		Contract: LeafContract{
			Identity: contract.ToolIdentitySpec{
				ProductID: "dev", Name: "query_de_trace",
				CanonicalPath: "dev.query_de_trace",
				CLIPath: "dev deap-agent trace", PrimaryCLIPath: "dev deap-agent trace",
				Group: "deap-agent",
			},
			Description: "按 traceId 查询完整 DEAP Langfuse 链路；服务端先执行调用人两级授权。",
			DryRun: deapAgentDryRun,
			Interface: deapAgentMCPInterface(deapAgentTraceTool),
			Selection: contract.SelectionSpec{
				AgentSummary: "在服务端授权后查询数字员工完整 Langfuse 链路",
				UseWhen: []string{"已有 EagleEye runId 或 Langfuse traceId，需要排查完整模型链路时"},
				AvoidWhen: []string{"只需单聊状态时使用 run-status", "只有 openMessageId 时先使用 resolve-run-id"},
				Examples: []string{"dws dev deap-agent trace --trace-id <traceId> --format json"},
			},
		},
	})
}

func deapAgentCallWithProfile(_ *cobra.Command, tool string, args map[string]any) error {
	profileValue, profileProvided := args["digitalTagEmployeeProfile"]
	profile := map[string]any{}
	if existing, ok := profileValue.(map[string]any); ok {
		for key, value := range existing {
			profile[key] = value
		}
	}
	for _, field := range []struct {
		argument string
		property string
	}{
		{"digitalTagEmployeeProfile.employeeNo", "employeeNo"},
		{"digitalTagEmployeeProfile.positionName", "positionName"},
		{"digitalTagEmployeeProfile.directSupervisorUid", "directSupervisorUid"},
		{"digitalTagEmployeeProfile.responseMode", "responseMode"},
	} {
		if value, ok := args[field.argument]; ok {
			profile[field.property] = value
			delete(args, field.argument)
		}
	}
	if profileProvided || len(profile) > 0 {
		args["digitalTagEmployeeProfile"] = profile
	}
	return callMCPToolOnServer(deapAgentServerID, tool, args)
}

func deapAgentMCPInterface(tool string) *contract.InterfaceSpec {
	return &contract.InterfaceSpec{
		Mode: contract.InterfaceModeMCP, Availability: contract.InterfaceAvailable,
		Ref: &contract.InterfaceRefSpec{ProductID: deapAgentServerID, RPCName: tool},
	}
}

func deapAgentNoArgs(cmd *cobra.Command) {
	cmd.Args = cobra.NoArgs
}

func deapAgentMaxRunes(cmd *cobra.Command, flagName string, max int) error {
	value, _ := cmd.Flags().GetString(flagName)
	value = strings.TrimSpace(value)
	if value != "" && utf8.RuneCountInString(value) > max {
		return apperrors.NewValidation(fmt.Sprintf("参数 --%s 最多允许 %d 个 Unicode 码点", flagName, max))
	}
	return nil
}

func deapAgentJSONObject(raw string) (any, error) {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, apperrors.NewValidation("参数必须是合法 JSON 对象：" + err.Error())
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, apperrors.NewValidation("参数必须解码为 JSON 对象")
	}
	return object, nil
}

func deapAgentJSONArray(raw string) (any, error) {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, apperrors.NewValidation("参数必须是合法 JSON 数组：" + err.Error())
	}
	array, ok := value.([]any)
	if !ok {
		return nil, apperrors.NewValidation("参数必须解码为 JSON 数组")
	}
	return array, nil
}

func deapAgentMessageIDsJSON(raw string) (any, error) {
	value, err := deapAgentJSONArray(raw)
	if err != nil {
		return nil, err
	}
	messageIDs := value.([]any)
	if len(messageIDs) == 0 {
		return nil, apperrors.NewValidation("message-ids-json 至少包含一个用户消息 ID")
	}
	for index, item := range messageIDs {
		messageID, ok := item.(string)
		if !ok || strings.TrimSpace(messageID) == "" {
			return nil, apperrors.NewValidation(fmt.Sprintf("message-ids-json[%d] 必须是非空字符串", index))
		}
	}
	return messageIDs, nil
}

func deapAgentProfileJSON(raw string) (any, error) {
	value, err := deapAgentJSONObject(raw)
	if err != nil {
		return nil, err
	}
	profile := value.(map[string]any)
	allowed := map[string]bool{
		"employeeNo": true, "positionName": true,
		"directSupervisorUid": true, "responseMode": true,
	}
	for key := range profile {
		if !allowed[key] {
			return nil, apperrors.NewValidation(fmt.Sprintf("profile-json 不接受字段 %q", key))
		}
	}
	if employeeNo, ok := profile["employeeNo"].(string); ok && utf8.RuneCountInString(employeeNo) > 64 {
		return nil, apperrors.NewValidation("profile.employeeNo 最多允许 64 个 Unicode 码点")
	}
	if position, ok := profile["positionName"].(string); ok && utf8.RuneCountInString(position) > 128 {
		return nil, apperrors.NewValidation("profile.positionName 最多允许 128 个 Unicode 码点")
	}
	if mode, ok := profile["responseMode"].(string); ok && mode != "mention_only" && mode != "targeted_proactive" {
		return nil, apperrors.NewValidation("profile.responseMode 只允许 mention_only 或 targeted_proactive")
	}
	return profile, nil
}
