package helpers

import (
	"fmt"

	"github.com/spf13/cobra"
)

// ──────────────────────────────────────────────────────────
// dws deap run — 数字员工执行观测
//
// 三个容易搞错的点，在这里说明一次，各子命令的 Long 里也各自重复：
//
//  1. 状态查询按场域分两个口径。单聊走 DEAP 既有链路，服务端生成 taskId 并登记状态，
//     可直接用 `run status` 轮询；群内走 Runtime V3 入口，taskId 是调用方传进去的、
//     服务端不登记，只能用消息 ID 经 `run executions` 现查。传错口径会稳定查不到。
//  2. 反查用的是 openMessageId，不是 openTaskId。openTaskId 是发消息接口返回的发送句柄，
//     openMessageId 是消息投递后 IM 才生成的业务键。手上只有 openTaskId 时，
//     需先用 `dws chat message query-send-status` 换取 openMessageId。
//  3. trace 含完整对话内容，权限比状态更严。服务端先做两级权限校验再取内容：
//     管理者可看该数字员工全部执行，使用者仅可看自己触发的。
// ──────────────────────────────────────────────────────────

func newDeapRunCommand() *cobra.Command {
	runCmd := &cobra.Command{Use: "run", Short: "数字员工执行观测", RunE: groupRunE}

	runStatusCmd := &cobra.Command{
		Use:   "status",
		Short: "查询单次执行状态（单聊口径）",
		Long: `按 taskId 查询数字员工单次执行的状态（运行中 / 成功 / 失败 / 已中止）。

仅适用于单聊：单聊由服务端生成 taskId 并登记状态所以可轮询；群内触发的执行服务端
不登记 taskId，要改用 ` + "`run executions`" + ` 按消息 ID 查，否则会稳定查不到。`,
		Example: "  dws deap run status --assistant <assistantId> --task <taskId>",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateRequiredFlagWithAliases(cmd, "assistant", "id"); err != nil {
				return err
			}
			if err := validateRequiredFlagWithAliases(cmd, "task"); err != nil {
				return err
			}
			task, _ := cmd.Flags().GetString("task")
			return callMCPTool("query_digital_employee_run_status", map[string]any{
				"assistantId": flagOrFallback(cmd, "assistant", "id"),
				"taskId":      task,
			})
		},
	}
	runStatusCmd.Flags().String("assistant", "", "数字员工 ID (assistantId)")
	runStatusCmd.Flags().String("id", "", "数字员工 ID (assistant 的别名)")
	runStatusCmd.Flags().String("task", "", "任务 ID (taskId，由服务端生成)")
	DeclareLeafMetadata(runStatusCmd, LeafSpec{
		Safety: deapReadSafety(),
		Contract: deapLeafContract("query_digital_employee_run_status", "deap run status",
			"按 taskId 查询数字员工单次执行状态（单聊口径）",
			"要查单聊中某次执行的状态、且手上有服务端返回的 taskId 时",
			"群内触发的执行没有可查的 taskId，改用 run executions 按消息 ID 查",
			"dws deap run status --assistant <assistantId> --task <taskId>"),
	})

	runExecutionsCmd := &cobra.Command{
		Use:   "executions",
		Short: "批量查询执行记录（群内口径）",
		Long: `按用户消息 ID 批量查询数字员工执行记录（状态、触发来源、触发规则）。
这是群内场域唯一可查的口径。

传的必须是发起本次执行的用户消息 ID，不能传助手消息 ID——触发源只写在用户消息上，
传错会查到一条缺触发源的记录。查不到的消息 ID 不会出现在结果里，需自行判断缺失。`,
		Example: "  dws deap run executions --messages <messageId1>,<messageId2>",
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, _ := cmd.Flags().GetStringSlice("messages")
			if len(ids) == 0 {
				return fmt.Errorf("必须提供 --messages")
			}
			return callMCPTool("query_digital_employee_run_executions", map[string]any{
				"messageIds": ids,
			})
		},
	}
	runExecutionsCmd.Flags().StringSlice("messages", nil, "用户消息 ID 列表（发起执行的那条用户消息，非助手消息）")
	DeclareLeafMetadata(runExecutionsCmd, LeafSpec{
		Safety: deapReadSafety(),
		Contract: deapLeafContract("query_digital_employee_run_executions", "deap run executions",
			"按用户消息 ID 批量查询数字员工执行记录（群内口径）",
			"要查群内触发的执行状态、或一次查多条执行记录时",
			"单聊场景手上有 taskId 时用 run status 更直接",
			"dws deap run executions --messages <messageId1>,<messageId2>"),
	})

	runResolveCmd := &cobra.Command{
		Use:   "resolve",
		Short: "按来源 ID 反查执行 runId",
		Long: `用钉钉消息 ID（openMessageId）找到它对应的那次执行，以便进一步拉 trace。

注意传的是 openMessageId 而不是 openTaskId：后者是发消息接口返回的发送句柄，
需先用 ` + "`dws chat message query-send-status --open-task-id <openTaskId>`" + ` 换成 openMessageId。
未建立映射时返回空而不报错——无法从 ID 形态预判是否存在映射。`,
		Example: "  dws deap run resolve --source <openMessageId>",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateRequiredFlagWithAliases(cmd, "source"); err != nil {
				return err
			}
			source, _ := cmd.Flags().GetString("source")
			toolArgs := map[string]any{"sourceId": source}
			// 不传时由服务端按 im_message 解读；这里不代填，避免把默认值固化在两处
			if v, _ := cmd.Flags().GetString("source-type"); v != "" {
				toolArgs["sourceType"] = v
			}
			return callMCPTool("resolve_digital_employee_run", toolArgs)
		},
	}
	runResolveCmd.Flags().String("source", "", "来源侧原始 ID（钉钉场域填 openMessageId）")
	runResolveCmd.Flags().String("source-type", "", "来源类型：im_message / trigger_rule / scenario_instance，默认 im_message")
	DeclareLeafMetadata(runResolveCmd, LeafSpec{
		Safety: deapReadSafety(),
		Contract: deapLeafContract("resolve_digital_employee_run", "deap run resolve",
			"按来源 ID 反查执行 runId",
			"只有钉钉消息 ID、要找到对应执行的 runId 以便拉 trace 时",
			"手上是 openTaskId 时先用 chat 的发送状态查询换成 openMessageId，本命令不接受 openTaskId",
			"dws deap run resolve --source <openMessageId>"),
	})

	runTraceCmd := &cobra.Command{
		Use:   "trace",
		Short: "拉取一次执行的完整 trace",
		Long: `拉取数字员工某次执行的完整 trace（模型调用、工具调用、中间结果），用于排查回复过程。

trace 含完整对话内容，权限比状态查询更严：管理者可看该数字员工全部执行，
使用者仅可看自己触发的，无权时直接拒绝且不会读取内容。
traceId 可从 ` + "`run resolve`" + ` 或执行记录中获得，30 位与 32 位两种形态都接受。`,
		Example: "  dws deap run trace --trace <traceId>",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateRequiredFlagWithAliases(cmd, "trace", "run-id"); err != nil {
				return err
			}
			return callMCPTool("query_digital_employee_trace", map[string]any{
				"traceId": flagOrFallback(cmd, "trace", "run-id"),
			})
		},
	}
	runTraceCmd.Flags().String("trace", "", "执行的 traceId（即 runId），30 位或 32 位形态均可")
	runTraceCmd.Flags().String("run-id", "", "runId (trace 的别名)")
	DeclareLeafMetadata(runTraceCmd, LeafSpec{
		Safety: deapReadSafety(),
		Contract: deapLeafContract("query_digital_employee_trace", "deap run trace",
			"拉取一次执行的完整 trace",
			"要排查某次执行的详细过程、看模型与工具调用链路时",
			"只想知道成功/失败时用 run status 或 run executions，trace 数据量大且更敏感",
			"dws deap run trace --trace <traceId>"),
	})

	runCmd.AddCommand(runStatusCmd, runExecutionsCmd, runResolveCmd, runTraceCmd)
	return runCmd
}
