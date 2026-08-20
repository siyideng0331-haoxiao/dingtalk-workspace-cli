package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/cli"
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

type localRunnerStartLocalOptions struct {
	AgentRef      string
	LocalAgentID  string
	DisplayName   string
	OpenAPIBase   string
	MaxConcurrent int
	Streaming     bool
}

type localRunnerA2ALocalRunner struct {
	RunnerID   string `json:"runnerId"`
	EndpointID string `json:"endpointId"`
	Status     string `json:"status"`
}

type localRunnerA2AConfiguration struct {
	Type         string                    `json:"type"`
	AgentCardURL string                    `json:"agentCardUrl"`
	LocalRunner  localRunnerA2ALocalRunner `json:"localRunner"`
}

type localRunnerStartLocalResult struct {
	Summary        localRunnerA2AConfiguration
	ConnectOptions localRunnerConnectOptions
	Close          func() error
}

type localRunnerCommandRuntime interface {
	Expose(context.Context, localRunnerExposeOptions) (*localrunner.CreatedRunner, error)
	Status(context.Context, string) (*localRunnerStatusResult, error)
	Revoke(context.Context, string) (*localrunner.RevokeRunnerData, error)
	StartLocal(context.Context, localRunnerStartLocalOptions) (*localRunnerStartLocalResult, error)
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
	runtime := &cobra.Command{Use: "runtime", Short: "运行 DEAP 本地 Agent"}
	runtime.AddCommand(newLocalRunnerStartLocalCommand())
	deap.AddCommand(localRunner, runtime)
	return deap
}

func newLocalRunnerStartLocalCommand() *cobra.Command {
	options := localRunnerStartLocalOptions{
		OpenAPIBase:   defaultLocalRunnerOpenAPIBase,
		MaxConcurrent: 4,
		Streaming:     true,
	}
	cmd := &cobra.Command{
		Use:   "start-local <agent-ref>",
		Short: "注册并运行一个本地 A2A Agent",
		Long:  "使用 test-echo 在当前 dws 进程启动内置验收 Agent，或读取一个 loopback Agent Card URL 兼容外部真实 Agent；注册后先输出不含凭证的公网 A2A 配置，再维持 WSS 与本地 HTTP/SSE 代理。",
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			options.AgentRef = strings.TrimSpace(args[0])
			runtime := localRunnerCommandRuntimeProvider()
			result, err := runtime.StartLocal(cmd.Context(), options)
			if err != nil {
				return err
			}
			if result.Close != nil {
				defer result.Close()
			}
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result.Summary); err != nil {
				return err
			}
			_, err = runtime.Connect(cmd.Context(), result.ConnectOptions)
			return err
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&options.LocalAgentID, "local-agent-id", "", "本地 Agent 的稳定 ID；test-echo 默认使用固定 ID，URL 模式确定性派生")
	flags.StringVar(&options.DisplayName, "display-name", "", "LocalRunner 显示名称；默认使用 Agent Card name")
	flags.StringVar(&options.OpenAPIBase, "openapi-base", defaultLocalRunnerOpenAPIBase, "DEAP LocalRunner OpenAPI base URL")
	flags.IntVar(&options.MaxConcurrent, "max-concurrent", 4, "最大并发 A2A 请求数")
	flags.BoolVar(&options.Streaming, "streaming", true, "声明支持 SSE streaming")
	positionals := []contract.RuntimeSchemaPositional{{
		Name: "agent_ref", Type: "string", Description: "内置 test-echo 或本地 loopback Agent Card URL", Required: true, Index: 0,
	}}
	cli.AnnotateRuntimePositionals(cmd, positionals...)
	declaration := localRunnerContract(
		"runtime_start_local",
		"deap.runtime_start_local",
		"deap runtime start-local",
		"从内置 test-echo 或本地 Agent Card 一键注册并维持公网 A2A LocalRunner 连接",
		"需要单进程验收 test-echo，或已有可访问的 loopback Agent Card 并需一次完成注册、配置输出和长连接代理时",
		"任意 agent-id 与进程监督尚不支持；只注册不用长连接时使用 deap local-runner expose，已有 Runner 重连时使用 connect",
		[]string{"dws deap runtime start-local test-echo", "dws deap runtime start-local http://127.0.0.1:8000/.well-known/agent-card.json"},
	)
	declaration.Positionals = positionals
	declaration.Parameters = []contract.ParamDecl{
		{Name: "local-agent-id", Property: "localAgentId", InterfaceType: "string", Description: "本地 Agent 的稳定 ID；test-echo 默认固定，URL 模式确定性派生"},
		{Name: "display-name", Property: "displayName", InterfaceType: "string", Description: "LocalRunner 显示名称；默认使用 Agent Card name"},
		{Name: "openapi-base", Property: "openApiBase", InterfaceType: "string", Description: "DEAP LocalRunner OpenAPI base URL"},
		{Name: "max-concurrent", Property: "maxConcurrent", InterfaceType: "integer", Description: "最大并发 A2A 请求数"},
		{Name: "streaming", Property: "streaming", InterfaceType: "boolean", Description: "声明支持 SSE streaming"},
	}
	helpers.DeclareLeafMetadata(cmd, helpers.LeafSpec{
		Safety:   contract.SafetySpec{Effect: "write", Risk: "medium", Confirmation: "not_required", Idempotency: "non_idempotent"},
		Contract: declaration,
	})
	return cmd
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
