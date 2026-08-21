package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	AgentKind    string
	WorkDir      string
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
	AgentCommand  string
	WorkDir       string
	Model         string
	LocalAgentID  string
	DisplayName   string
	RunnerID      string
	EndpointID    string
	OpenAPIBase   string
	MaxConcurrent int
	Streaming     bool
	Memory        bool
	Yolo          bool
	AgentTimeout  time.Duration
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
	runtime := &cobra.Command{Use: "runtime", Short: "运行和管理 DEAP 本地 Agent"}
	runtime.AddCommand(
		newLocalRunnerExposeCommand(),
		newLocalRunnerStatusCommand(),
		newLocalRunnerRevokeCommand(),
		newLocalRunnerConnectCommand(),
		newLocalRunnerStartLocalCommand(),
	)
	deap.AddCommand(runtime)
	return deap
}

func newLocalRunnerStartLocalCommand() *cobra.Command {
	var harness string
	harnesses := helpers.LocalRunnerAgentChannels()
	harnessDescription := "本地 Agent harness；支持 " + strings.Join(harnesses, ", ")
	options := localRunnerStartLocalOptions{
		MaxConcurrent: 4,
		Streaming:     true,
		Memory:        true,
		Yolo:          true,
	}
	cmd := &cobra.Command{
		Use:   "start-local",
		Short: "注册并运行一个本地 A2A Agent",
		Long:  "使用 --harness 在当前 dws 进程管理指定项目目录中的本地 Agent。支持 " + strings.Join(harnesses, ", ") + "；全部复用 dev connect 的共享 backend。custom 需要 --agent-cmd 或 DWS_AGENT_CMD。日常重启可直接重跑原命令并按稳定本地身份隐式恢复；本地配置丢失或迁移时，可成对提供 --runner-id 与 --endpoint-id 做灾难恢复。注册或恢复后先输出不含凭证的公网 A2A 配置，再维持 WSS 与本地 HTTP/SSE 代理。",
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options.AgentRef = strings.TrimSpace(harness)
			options.AgentCommand = strings.TrimSpace(options.AgentCommand)
			options.RunnerID = strings.TrimSpace(options.RunnerID)
			options.EndpointID = strings.TrimSpace(options.EndpointID)
			if (options.RunnerID == "") != (options.EndpointID == "") {
				return ErrLocalRunnerRuntimeInvalid
			}
			if !helpers.IsLocalRunnerAgentChannel(options.AgentRef) {
				return ErrLocalRunnerRuntimeInvalid
			}
			if options.AgentRef == "custom" {
				if options.AgentCommand == "" && strings.TrimSpace(os.Getenv("DWS_AGENT_CMD")) == "" {
					return ErrLocalRunnerRuntimeInvalid
				}
			} else if options.AgentCommand != "" {
				return ErrLocalRunnerRuntimeInvalid
			}
			workDir, err := normalizeLocalRunnerWorkDir(options.WorkDir)
			if err != nil {
				return err
			}
			options.WorkDir = workDir
			options.Model = strings.TrimSpace(options.Model)
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
	flags.StringVar(&harness, "harness", "", harnessDescription)
	flags.StringVar(&options.WorkDir, "work-dir", "", "本地 Agent 项目目录；可使用相对或绝对路径")
	flags.StringVar(&options.AgentCommand, "agent-cmd", "", "custom harness 的无头命令；问题作为末参，stdout 作为回复；也可使用 DWS_AGENT_CMD")
	flags.StringVar(&options.RunnerID, "runner-id", "", "灾难恢复已有 Runner；必须与 --endpoint-id 成对提供")
	flags.StringVar(&options.EndpointID, "endpoint-id", "", "灾难恢复已有 Endpoint；必须与 --runner-id 成对提供")
	flags.StringVar(&options.Model, "model", "", "本地 Agent 模型覆盖；留空使用 channel 默认模型")
	flags.StringVar(&options.LocalAgentID, "local-agent-id", "", "本地 Agent 的稳定 ID；默认按 harness/绝对 work-dir 确定性派生")
	flags.StringVar(&options.DisplayName, "display-name", "", "LocalRunner 显示名称；默认使用 Agent Card name")
	flags.IntVar(&options.MaxConcurrent, "max-concurrent", 4, "最大并发 A2A 请求数")
	flags.BoolVar(&options.Streaming, "streaming", true, "声明支持 SSE streaming")
	flags.BoolVar(&options.Memory, "memory", true, "按 A2A contextId 复用本地 Agent session")
	flags.BoolVar(&options.Yolo, "yolo", true, "启用本地 Agent 的最高权限模式；设为 false 使用受限模式")
	flags.DurationVar(&options.AgentTimeout, "agent-timeout", 0, "单次本地 Agent 推理超时；0 表示使用 backend 默认值")
	_ = cmd.MarkFlagRequired("harness")
	_ = cmd.MarkFlagRequired("work-dir")
	cmd.MarkFlagsRequiredTogether("runner-id", "endpoint-id")
	declaration := localRunnerContract(
		"runtime_start_local",
		"deap.runtime_start_local",
		"deap runtime start-local",
		"从共享本地 Agent backend 一键注册并维持公网 A2A LocalRunner 连接",
		"需要把指定项目目录中的受支持本地 Agent harness 暴露为 A2A 时",
		"只注册不用长连接时使用 deap runtime expose，已有 Runner 重连时使用 connect；auto、外部 connector 和远程 API 不是 LocalRunner harness",
		[]string{"dws deap runtime start-local --harness opencode --work-dir ./project", "dws deap runtime start-local --harness opencode --work-dir ./project --runner-id lr_01 --endpoint-id lre_01"},
	)
	required := true
	optional := false
	declaration.Parameters = []contract.ParamDecl{
		{Name: "harness", Property: "harness", InterfaceType: "string", Description: harnessDescription, Required: &required, Enum: append([]string(nil), harnesses...)},
		{Name: "work-dir", Property: "workDir", InterfaceType: "string", Description: "本地 Agent 项目目录；可使用相对或绝对路径", Required: &required},
		{Name: "agent-cmd", Property: "agentCommand", InterfaceType: "string", Description: "custom harness 的无头命令；仅在内存中传给共享 backend，也可使用 DWS_AGENT_CMD", Required: &optional, RequiredWhen: "harness=custom unless DWS_AGENT_CMD is set"},
		{Name: "runner-id", Property: "runnerId", InterfaceType: "string", Description: "灾难恢复已有 Runner；必须与 endpoint-id 成对提供", Required: &optional, RequiredWhen: "endpoint-id is provided"},
		{Name: "endpoint-id", Property: "endpointId", InterfaceType: "string", Description: "灾难恢复已有 Endpoint；必须与 runner-id 成对提供", Required: &optional, RequiredWhen: "runner-id is provided"},
		{Name: "model", Property: "model", InterfaceType: "string", Description: "本地 Agent 模型覆盖；留空使用 channel 默认模型"},
		{Name: "local-agent-id", Property: "localAgentId", InterfaceType: "string", Description: "本地 Agent 的稳定 ID；默认按 harness/绝对 work-dir 确定性派生"},
		{Name: "display-name", Property: "displayName", InterfaceType: "string", Description: "LocalRunner 显示名称；默认使用 Agent Card name"},
		{Name: "max-concurrent", Property: "maxConcurrent", InterfaceType: "integer", Description: "最大并发 A2A 请求数"},
		{Name: "streaming", Property: "streaming", InterfaceType: "boolean", Description: "声明支持 SSE streaming"},
		{Name: "memory", Property: "memory", InterfaceType: "boolean", Description: "按 A2A contextId 复用本地 Agent session"},
		{Name: "yolo", Property: "yolo", InterfaceType: "boolean", Description: "启用本地 Agent 的最高权限模式"},
		{Name: "agent-timeout", Property: "agentTimeout", InterfaceType: "duration", Description: "单次本地 Agent 推理超时；0 使用 backend 默认值"},
	}
	helpers.DeclareLeafMetadata(cmd, helpers.LeafSpec{
		Safety:   contract.SafetySpec{Effect: "write", Risk: "medium", Confirmation: "not_required", Idempotency: "non_idempotent"},
		Contract: declaration,
	})
	return cmd
}

func normalizeLocalRunnerWorkDir(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", ErrLocalRunnerRuntimeInvalid
	}
	absolute, err := filepath.Abs(strings.TrimSpace(raw))
	if err != nil {
		return "", ErrLocalRunnerRuntimeInvalid
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return "", ErrLocalRunnerRuntimeInvalid
	}
	return absolute, nil
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
		},
		Safety: contract.SafetySpec{Effect: "write", Risk: "medium", Confirmation: "not_required", Idempotency: "non_idempotent"},
		Contract: localRunnerContract(
			"runtime_expose",
			"deap.runtime_expose",
			"deap runtime expose",
			"注册一个本地 A2A Agent 并安全保存一次性 endpoint bearer",
			"已有可访问的 loopback Agent Card，需要获得标准公网 Agent Card URL 时",
			"只查询已有 Runner 使用 deap runtime status；已有 Runner 建连使用 connect",
			[]string{"dws deap runtime expose --local-agent-id <id> --display-name <name> --agent-card-url http://127.0.0.1:8000/.well-known/agent-card.json"},
		),
		Call: func(cmd *cobra.Command, _ string, args map[string]any) error {
			result, err := localRunnerCommandRuntimeProvider().Expose(cmd.Context(), localRunnerExposeOptions{
				LocalAgentID: stringToolArg(args, "localAgentId"),
				DisplayName:  stringToolArg(args, "displayName"),
				AgentCardURL: stringToolArg(args, "agentCardUrl"),
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
			"runtime_status",
			"deap.runtime_status",
			"deap runtime status",
			"查询 LocalRunner 与当前 WSS 连接状态",
			"需要确认 Runner 是否 ACTIVE、Endpoint 是否连接以及最近心跳时",
			"创建使用 expose；建立本地代理长连接使用 connect；撤销使用 revoke",
			[]string{"dws deap runtime status --runner-id <runnerId>"},
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
			"runtime_revoke",
			"deap.runtime_revoke",
			"deap runtime revoke",
			"撤销 LocalRunner 及其唯一 Endpoint 并关闭连接",
			"已确认不再需要该公网 Agent Card/RPC Endpoint，需要永久撤销时",
			"只断开当前 WSS 而保留 Endpoint 时不要使用 revoke",
			[]string{"dws deap runtime revoke --runner-id <runnerId>"},
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
			"runtime_connect",
			"deap.runtime_connect",
			"deap runtime connect",
			"使用新 ticket 建立单 Endpoint WSS 并代理本地 A2A HTTP/SSE",
			"Runner 已注册，需要保持公网 RPC 到本地 Agent 的长连接时",
			"只检查连接状态使用 status；尚未注册使用 expose",
			[]string{"dws deap runtime connect --runner-id <runnerId> --endpoint-id <endpointId> --target-url http://127.0.0.1:8000/rpc --agent-card-sha256 <sha256>"},
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
