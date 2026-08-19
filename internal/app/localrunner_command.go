package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/corecmd/contract"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/helpers"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/localrunner"
	"github.com/spf13/cobra"
)

type localRunnerExposeOptions struct {
	LocalAgentID string
	DisplayName  string
	AgentCardURL string
	OpenAPIBase  string
}

type localRunnerConnectOptions struct {
	RunnerID       string
	EndpointID     string
	TargetURL      string
	AgentCardSHA256 string
	MaxConcurrent  int
	Streaming      bool
}

type localRunnerCommandRuntime interface {
	Expose(context.Context, localRunnerExposeOptions) (*localrunner.CreatedRunner, error)
	Status(context.Context, string) (*localRunnerStatusResult, error)
	Revoke(context.Context, string) (*localrunner.RevokeRunnerData, error)
	Connect(context.Context, localRunnerConnectOptions) (*localrunner.ConnectionStateSnapshot, error)
}

var localRunnerCommandRuntimeProvider = func() localRunnerCommandRuntime {
	return newProductionLocalRunnerCommandRuntime(localRunnerRuntimeDependencies{})
}

func newDEAPCommand() *cobra.Command {
	contract.RegisterProductDecl(contract.ProductDecl{
		ID: "deap",
		Selection: contract.ProductSelectionDecl{
			AgentSummary: "管理 DEAP 本地与远程 Agent 能力",
			UseWhen:      []string{"需要把本地 A2A Agent 注册、查询、连接或撤销为 DEAP LocalRunner 时"},
			AvoidWhen:    []string{"普通钉钉应用、事件或文档操作应使用对应产品命令"},
		},
	})
	deap := &cobra.Command{Use: "deap", Short: "DEAP Agent 管理"}
	localRunner := &cobra.Command{Use: "local-runner", Short: "管理单端点 LocalRunner A2A 网关"}
	localRunner.AddCommand(
		newLocalRunnerExposeCommand(),
		newLocalRunnerStatusCommand(),
		newLocalRunnerRevokeCommand(),
		newLocalRunnerConnectCommand(),
	)
	deap.AddCommand(localRunner)
	return deap
}

func newLocalRunnerExposeCommand() *cobra.Command {
	return helpers.NewLeafCommand(helpers.LeafSpec{
		Use:   "expose",
		Short: "注册一个本地 A2A Agent 为 LocalRunner",
		Long:  "读取本地 Agent Card，创建严格一对一的 Runner/Endpoint，并把一次性 endpoint bearer 直接写入系统 keyring。",
		Tool:  "local_runner_expose",
		Flags: []helpers.LeafFlag{
			{Name: "local-agent-id", Usage: "本地 Agent 的稳定 ID", Bind: "localAgentId", Trim: true, Required: true, MarkRequired: true},
			{Name: "display-name", Usage: "LocalRunner 显示名称", Bind: "displayName", Trim: true, Required: true, MarkRequired: true},
			{Name: "agent-card-url", Usage: "本地 loopback Agent Card URL", Bind: "agentCardUrl", Trim: true, Required: true, MarkRequired: true, Format: "uri"},
			{Name: "openapi-base", Usage: "DEAP LocalRunner OpenAPI base URL", Bind: "openApiBase", Trim: true, Default: defaultLocalRunnerOpenAPIBase, ArgDefault: defaultLocalRunnerOpenAPIBase, Format: "uri"},
		},
		Safety: contract.SafetySpec{Effect: "write", Risk: "medium", Confirmation: "not_required", Idempotency: "non_idempotent"},
		Contract: localRunnerContract(
			"local_runner_expose",
			"deap.local_runner_expose",
			"deap local-runner expose",
			"注册一个本地 A2A Agent 并安全保存一次性 endpoint bearer",
			"已有可访问的 loopback Agent Card，需要获得标准公网 Agent Card URL 时",
			"只查询已有 Runner 使用 deap local-runner status；已有 Runner 建连使用 connect",
			[]string{"dws deap local-runner expose --local-agent-id <id> --display-name <name> --agent-card-url http://127.0.0.1:8000/.well-known/agent-card.json"},
		),
		Call: func(cmd *cobra.Command, _ string, args map[string]any) error {
			result, err := localRunnerCommandRuntimeProvider().Expose(cmd.Context(), localRunnerExposeOptions{
				LocalAgentID: stringToolArg(args, "localAgentId"),
				DisplayName:  stringToolArg(args, "displayName"),
				AgentCardURL: stringToolArg(args, "agentCardUrl"),
				OpenAPIBase:  stringToolArg(args, "openApiBase"),
			})
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	})
}

func newLocalRunnerStatusCommand() *cobra.Command {
	return helpers.NewLeafCommand(helpers.LeafSpec{
		Use:   "status",
		Short: "查询 LocalRunner 与连接状态",
		Long:  "查询严格一对一 Runner/Endpoint 的控制面状态；输出不包含任何 bearer 或 ticket。",
		Tool:  "local_runner_status",
		Flags: []helpers.LeafFlag{
			{Name: "runner-id", Usage: "Runner ID", Bind: "runnerId", Trim: true, Required: true, MarkRequired: true},
		},
		Safety: contract.SafetySpec{Effect: "read", Risk: "low", Confirmation: "not_required", Idempotency: "idempotent"},
		Contract: localRunnerContract(
			"local_runner_status",
			"deap.local_runner_status",
			"deap local-runner status",
			"查询 LocalRunner 与当前 WSS 连接状态",
			"需要确认 Runner 是否 ACTIVE、Endpoint 是否连接以及最近心跳时",
			"创建使用 expose；建立本地代理长连接使用 connect；撤销使用 revoke",
			[]string{"dws deap local-runner status --runner-id <runnerId>"},
		),
		Call: func(cmd *cobra.Command, _ string, args map[string]any) error {
			result, err := localRunnerCommandRuntimeProvider().Status(cmd.Context(), stringToolArg(args, "runnerId"))
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	})
}

func newLocalRunnerRevokeCommand() *cobra.Command {
	return helpers.NewLeafCommand(helpers.LeafSpec{
		Use:   "revoke",
		Short: "撤销 LocalRunner 与唯一 Endpoint",
		Long:  "撤销 Runner/Endpoint 绑定并要求服务端关闭活动连接；该操作不会输出或恢复 endpoint bearer。",
		Tool:  "local_runner_revoke",
		Flags: []helpers.LeafFlag{
			{Name: "runner-id", Usage: "Runner ID", Bind: "runnerId", Trim: true, Required: true, MarkRequired: true},
		},
		Safety: contract.SafetySpec{Effect: "destructive", Risk: "high", Confirmation: "user_required", Idempotency: "idempotent"},
		Contract: localRunnerContract(
			"local_runner_revoke",
			"deap.local_runner_revoke",
			"deap local-runner revoke",
			"撤销 LocalRunner 及其唯一 Endpoint 并关闭连接",
			"已确认不再需要该公网 Agent Card/RPC Endpoint，需要永久撤销时",
			"只断开当前 WSS 而保留 Endpoint 时不要使用 revoke",
			[]string{"dws deap local-runner revoke --runner-id <runnerId>"},
		),
		Call: func(cmd *cobra.Command, _ string, args map[string]any) error {
			result, err := localRunnerCommandRuntimeProvider().Revoke(cmd.Context(), stringToolArg(args, "runnerId"))
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	})
}

func newLocalRunnerConnectCommand() *cobra.Command {
	return helpers.NewLeafCommand(helpers.LeafSpec{
		Use:   "connect",
		Short: "连接一个 LocalRunner Endpoint 并代理到 localhost",
		Long:  "领取一次性 connection ticket，建立单 Endpoint WSS，并把不透明 A2A HTTP/SSE 字节代理到固定 loopback 目标。",
		Tool:  "local_runner_connect",
		Flags: []helpers.LeafFlag{
			{Name: "runner-id", Usage: "Runner ID", Bind: "runnerId", Trim: true, Required: true, MarkRequired: true},
			{Name: "endpoint-id", Usage: "与 Runner 严格一对一绑定的 Endpoint ID", Bind: "endpointId", Trim: true, Required: true, MarkRequired: true},
			{Name: "target-url", Usage: "固定 loopback A2A RPC URL", Bind: "targetUrl", Trim: true, Required: true, MarkRequired: true, Format: "uri"},
			{Name: "agent-card-sha256", Usage: "已注册 Agent Card 的 SHA-256", Bind: "agentCardSha256", Trim: true, Required: true, MarkRequired: true},
			{Name: "max-concurrent", Usage: "最大并发 A2A 请求数", Bind: "maxConcurrent", Kind: helpers.LeafInt, Default: "4", ArgDefault: "4"},
			{Name: "streaming", Usage: "声明支持 SSE streaming", Bind: "streaming", Kind: helpers.LeafBool, Default: "true"},
		},
		Safety: contract.SafetySpec{Effect: "write", Risk: "medium", Confirmation: "not_required", Idempotency: "unknown"},
		Contract: localRunnerContract(
			"local_runner_connect",
			"deap.local_runner_connect",
			"deap local-runner connect",
			"使用新 ticket 建立单 Endpoint WSS 并代理本地 A2A HTTP/SSE",
			"Runner 已注册，需要保持公网 RPC 到本地 Agent 的长连接时",
			"只检查连接状态使用 status；尚未注册使用 expose",
			[]string{"dws deap local-runner connect --runner-id <runnerId> --endpoint-id <endpointId> --target-url http://127.0.0.1:8000/rpc --agent-card-sha256 <sha256>"},
		),
		Call: func(cmd *cobra.Command, _ string, args map[string]any) error {
			streaming := true
			if value, ok := args["streaming"].(bool); ok {
				streaming = value
			}
			maxConcurrent := 4
			if value, ok := args["maxConcurrent"].(int); ok && value > 0 {
				maxConcurrent = value
			}
			result, err := localRunnerCommandRuntimeProvider().Connect(cmd.Context(), localRunnerConnectOptions{
				RunnerID:        stringToolArg(args, "runnerId"),
				EndpointID:      stringToolArg(args, "endpointId"),
				TargetURL:       stringToolArg(args, "targetUrl"),
				AgentCardSHA256: stringToolArg(args, "agentCardSha256"),
				MaxConcurrent:   maxConcurrent,
				Streaming:       streaming,
			})
			if err != nil {
				return err
			}
			output := struct {
				RunnerID     string                      `json:"runnerId"`
				EndpointID   string                      `json:"endpointId"`
				State        localrunner.ConnectionState `json:"state"`
				ConnectionID string                      `json:"connectionId,omitempty"`
			}{
				RunnerID:     result.Identity.RunnerID,
				EndpointID:   result.Identity.EndpointID,
				State:        result.State,
				ConnectionID: result.ConnectionID,
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(output)
		},
	})
}

func localRunnerContract(name, canonicalPath, cliPath, description, useWhen, avoidWhen string, examples []string) helpers.LeafContract {
	return helpers.LeafContract{
		Identity: contract.ToolIdentitySpec{
			ProductID:      "deap",
			Name:           name,
			CanonicalPath:  canonicalPath,
			CLIPath:        cliPath,
			PrimaryCLIPath: cliPath,
		},
		Description: description,
		Interface: &contract.InterfaceSpec{
			Mode:         "composite",
			Availability: "available",
			Reason:       "命令由 CLI 编排 OAuth OpenAPI 控制面、WSS 隧道与 loopback HTTP 代理，不绑定单一 MCP RPC",
		},
		Selection: contract.SelectionSpec{
			AgentSummary: description,
			UseWhen:      []string{useWhen},
			AvoidWhen:    []string{avoidWhen},
			Examples:     examples,
		},
	}
}

func stringToolArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}
