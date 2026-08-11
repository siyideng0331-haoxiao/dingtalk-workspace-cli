package helpers

import (
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/corecmd/contract"
	"github.com/spf13/cobra"
)

// ──────────────────────────────────────────────────────────
// dws deap — DEAP 数字员工
//
// DEAP 是承载数字员工（数字员工 / digital employee）的平台。本产品覆盖两条链路：
//   - employee：数字员工本体的生命周期（创建 / 配置 / 发布 / 删除）
//   - run：一次执行的观测（状态 / 来源反查 / 完整 trace）
//
// 身份与权限：DEAP 不使用 API Key。服务端用会话里的 orgId + userId 重建用户上下文，
// 权限判定与 DEAP 后台完全一致——管理者可见该数字员工全部执行，使用者仅可见自己
// 触发的；元信息仅管理者可读。因此这里不暴露任何身份参数。
// ──────────────────────────────────────────────────────────

// deapReadSafety is the shared safety spec for read-only leaves.
func deapReadSafety() contract.SafetySpec {
	return contract.SafetySpec{
		Effect: "read", Risk: "low",
		Confirmation: "not_required", Idempotency: "idempotent",
	}
}

// deapWriteSafety is the shared safety spec for leaves that change state.
func deapWriteSafety() contract.SafetySpec {
	return contract.SafetySpec{
		Effect: "write", Risk: "medium",
		Confirmation: "user_required", Idempotency: "unknown",
	}
}

// deapLeafContract assembles the leaf contract, keeping identity paths in sync so
// a rename cannot silently diverge between them.
func deapLeafContract(tool, cliPath, desc, useWhen, avoidWhen, example string) LeafContract {
	return LeafContract{
		Identity: contract.ToolIdentitySpec{
			ProductID:      "deap",
			Name:           tool,
			CanonicalPath:  "deap." + tool,
			CLIPath:        cliPath,
			PrimaryCLIPath: cliPath,
		},
		Description: desc,
		Interface: &contract.InterfaceSpec{
			Mode:         "mcp",
			Availability: "available",
			Ref:          &contract.InterfaceRefSpec{ProductID: "deap", RPCName: tool},
		},
		Selection: contract.SelectionSpec{
			AgentSummary: desc,
			UseWhen:      []string{useWhen},
			AvoidWhen:    []string{avoidWhen},
			Examples:     []string{example},
		},
	}
}

// deapProfileArgs collects the employee profile sub-object shared by create and
// save-draft. Returns nil when the caller supplied none of the fields, so the key
// is omitted rather than sent as an empty object — under save-draft's overwrite
// semantics an empty object would wipe the stored profile.
func deapProfileArgs(cmd *cobra.Command) map[string]any {
	profile := map[string]any{}
	for flag, key := range map[string]string{
		"employee-no":    "employeeNo",
		"position-name":  "positionName",
		"supervisor-uid": "directSupervisorUid",
		"response-mode":  "responseMode",
	} {
		if v, _ := cmd.Flags().GetString(flag); v != "" {
			profile[key] = v
		}
	}
	if len(profile) == 0 {
		return nil
	}
	return profile
}

func newDeapCommand() *cobra.Command {
	// 产品级 Agent 路由声明。deap 是全新产品，未注册会让 Schema 装配报
	// missing ProductDecl，整个 help / catalog 随之不可用。三段 Selection 均必填：
	// 声明过的产品是最终路由来源，没有兜底文案可回退。
	contract.RegisterProductDecl(contract.ProductDecl{
		ID: "deap",
		Selection: contract.ProductSelectionDecl{
			AgentSummary: "管理 DEAP 数字员工（创建 / 配置 / 发布 / 删除）并观测其每次执行（状态、来源反查、完整 trace）",
			UseWhen: []string{
				"用户要新建、修改、发布或下线数字员工，或要排查数字员工某次回复为什么这样、跑到哪一步、由谁触发",
			},
			AvoidWhen: []string{
				"要收发普通群聊消息或操作钉钉助理（agentType=1）时使用 chat；数字员工的对话内容本身仍在 chat 侧查看",
			},
		},
	})

	// ===== employee：生命周期 =====

	employeeCmd := &cobra.Command{Use: "employee", Short: "数字员工生命周期", RunE: groupRunE}

	employeeListCmd := &cobra.Command{
		Use:     "list",
		Short:   "查询数字员工列表",
		Long:    "分页查询当前身份可见的数字员工。可见范围与 DEAP 后台一致：管理员看本租户全部，非管理员只看自己参与的。",
		Example: "  dws deap employee list\n  dws deap employee list --keyword \"招聘\" --page-size 50",
		RunE: func(cmd *cobra.Command, args []string) error {
			page, _ := cmd.Flags().GetInt("page")
			pageSize, _ := cmd.Flags().GetInt("page-size")
			args2 := map[string]any{"page": page, "pageSize": pageSize}
			if v, _ := cmd.Flags().GetString("keyword"); v != "" {
				args2["keyword"] = v
			}
			return callMCPTool("list_digital_employees", args2)
		},
	}
	employeeListCmd.Flags().String("keyword", "", "模糊搜索词，匹配员工名称 / 岗位名称 / 工号")
	employeeListCmd.Flags().Int("page", 1, "页码")
	employeeListCmd.Flags().Int("page-size", 20, "每页条数")
	DeclareLeafMetadata(employeeListCmd, LeafSpec{
		Safety: deapReadSafety(),
		Contract: deapLeafContract("list_digital_employees", "deap employee list",
			"分页查询当前身份可见的数字员工",
			"需要看有哪些数字员工，或需要先拿到 assistantId 再做后续操作时",
			"要查的是钉钉助理（agentType=1）而非数字员工时，本命令查不到",
			"dws deap employee list --keyword \"招聘\""),
	})

	employeeGetCmd := &cobra.Command{
		Use:     "get",
		Short:   "查询数字员工详情",
		Long:    "读取数字员工的完整配置（名称、描述、岗位、响应模式、人设提示词等）。非数字员工会返回不存在，因此也可用它判定类型。",
		Example: "  dws deap employee get --assistant <assistantId>",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateRequiredFlagWithAliases(cmd, "assistant", "id"); err != nil {
				return err
			}
			return callMCPTool("get_digital_employee", map[string]any{
				"assistantId": flagOrFallback(cmd, "assistant", "id"),
			})
		},
	}
	employeeGetCmd.Flags().String("assistant", "", "数字员工 ID (assistantId)")
	employeeGetCmd.Flags().String("id", "", "数字员工 ID (assistant 的别名)")
	DeclareLeafMetadata(employeeGetCmd, LeafSpec{
		Safety: deapReadSafety(),
		Contract: deapLeafContract("get_digital_employee", "deap employee get",
			"读取数字员工的完整配置",
			"要看某个数字员工的完整配置，或在保存草稿前读回全量以免覆写丢字段时",
			"只需要列表概览时用 employee list，避免逐个查详情",
			"dws deap employee get --assistant <assistantId>"),
	})

	employeeCreateCmd := &cobra.Command{
		Use:     "create",
		Short:   "创建数字员工（草稿态）",
		Long:    "新建数字员工。创建出来是草稿态、还不会响应任何消息，需再执行 publish 才生效。岗位名称与响应模式可留到之后再填，但不补齐无法发布。",
		Example: "  dws deap employee create --name \"招聘助手小钉\" --description \"简历初筛\" --org-code HR --org-name \"人力资源部\"",
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, f := range []string{"name", "description", "org-code", "org-name"} {
				if err := validateRequiredFlagWithAliases(cmd, f); err != nil {
					return err
				}
			}
			name, _ := cmd.Flags().GetString("name")
			desc, _ := cmd.Flags().GetString("description")
			orgCode, _ := cmd.Flags().GetString("org-code")
			orgName, _ := cmd.Flags().GetString("org-name")
			toolArgs := map[string]any{
				"name": name, "description": desc,
				"orgCode": orgCode, "orgName": orgName,
			}
			if v, _ := cmd.Flags().GetString("icon"); v != "" {
				toolArgs["icon"] = v
			}
			if profile := deapProfileArgs(cmd); profile != nil {
				toolArgs["digitalTagEmployeeProfile"] = profile
			}
			return callMCPTool("create_digital_employee", toolArgs)
		},
	}
	employeeCreateCmd.Flags().String("name", "", "员工名称 (必填)")
	employeeCreateCmd.Flags().String("description", "", "职责描述 (必填)")
	employeeCreateCmd.Flags().String("org-code", "", "归属组织编码 (必填)")
	employeeCreateCmd.Flags().String("org-name", "", "归属组织名称 (必填)")
	employeeCreateCmd.Flags().String("icon", "", "头像，发布前必填")
	employeeCreateCmd.Flags().String("employee-no", "", "工号")
	employeeCreateCmd.Flags().String("position-name", "", "岗位名称，发布前必填")
	employeeCreateCmd.Flags().String("supervisor-uid", "", "直属上级钉钉 uid")
	employeeCreateCmd.Flags().String("response-mode", "", "响应模式：mention_only 仅被@时响应 / targeted_proactive 定向主动响应，发布前必填")
	DeclareLeafMetadata(employeeCreateCmd, LeafSpec{
		Safety: deapWriteSafety(),
		Contract: deapLeafContract("create_digital_employee", "deap employee create",
			"创建数字员工（草稿态）",
			"要新建数字员工时；创建后为草稿态，需再发布才生效",
			"要修改已有数字员工时用 employee save-draft，不要重复创建",
			"dws deap employee create --name \"招聘助手小钉\" --description \"简历初筛\" --org-code HR --org-name \"人力资源部\""),
	})

	employeeSaveDraftCmd := &cobra.Command{
		Use:   "save-draft",
		Short: "保存数字员工草稿（不发布）",
		Long: `保存数字员工配置但不生效。

注意这是全量覆写：未传的字段会被清空。做增量修改必须先 employee get 读回全量、
改动其中几项后整体回传，否则会抹掉原有配置。保存后需再执行 publish 才生效。`,
		Example: "  dws deap employee save-draft --assistant <assistantId> --prompt \"你是一名招聘助手\"",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateRequiredFlagWithAliases(cmd, "assistant", "id"); err != nil {
				return err
			}
			toolArgs := map[string]any{"agentUuid": flagOrFallback(cmd, "assistant", "id")}
			for flag, key := range map[string]string{
				"name":        "name",
				"description": "description",
				"org-code":    "orgCode",
				"org-name":    "orgName",
				"icon":        "icon",
				"prompt":      "prompt",
			} {
				if v, _ := cmd.Flags().GetString(flag); v != "" {
					toolArgs[key] = v
				}
			}
			if profile := deapProfileArgs(cmd); profile != nil {
				toolArgs["digitalTagEmployeeProfile"] = profile
			}
			return callMCPTool("save_digital_employee_draft", toolArgs)
		},
	}
	employeeSaveDraftCmd.Flags().String("assistant", "", "数字员工 ID (assistantId)")
	employeeSaveDraftCmd.Flags().String("id", "", "数字员工 ID (assistant 的别名)")
	employeeSaveDraftCmd.Flags().String("name", "", "员工名称")
	employeeSaveDraftCmd.Flags().String("description", "", "职责描述")
	employeeSaveDraftCmd.Flags().String("org-code", "", "归属组织编码")
	employeeSaveDraftCmd.Flags().String("org-name", "", "归属组织名称")
	employeeSaveDraftCmd.Flags().String("icon", "", "头像，发布前必填")
	employeeSaveDraftCmd.Flags().String("prompt", "", "人设提示词，发布前必填")
	employeeSaveDraftCmd.Flags().String("employee-no", "", "工号")
	employeeSaveDraftCmd.Flags().String("position-name", "", "岗位名称，发布前必填")
	employeeSaveDraftCmd.Flags().String("supervisor-uid", "", "直属上级钉钉 uid")
	employeeSaveDraftCmd.Flags().String("response-mode", "", "响应模式，发布前必填")
	DeclareLeafMetadata(employeeSaveDraftCmd, LeafSpec{
		Safety: deapWriteSafety(),
		Contract: deapLeafContract("save_digital_employee_draft", "deap employee save-draft",
			"保存数字员工草稿（不发布）",
			"要改数字员工配置时；先读详情再整体回传以免覆写丢字段，保存后需再发布",
			"只想让已保存的草稿生效时直接用 employee publish，无需重复保存",
			"dws deap employee save-draft --assistant <assistantId> --prompt \"你是一名招聘助手\""),
	})

	employeePublishCmd := &cobra.Command{
		Use:   "publish",
		Short: "发布数字员工（使当前草稿生效）",
		Long: `让数字员工的当前草稿正式生效、开始响应消息。

发布只消费 ID 与是否允许入群，其余配置一律以已保存的草稿为准——要改配置先执行 save-draft。
发布前有一组必填校验（名称、头像、描述、组织编码、岗位名称、响应模式、人设提示词），
缺任一项会被拒且不产生任何副作用，补齐后可重试。`,
		Example: "  dws deap employee publish --assistant <assistantId>",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateRequiredFlagWithAliases(cmd, "assistant", "id"); err != nil {
				return err
			}
			noJoin, _ := cmd.Flags().GetBool("no-join-group")
			// 服务端缺省允许入群；用反向开关表达，避免布尔标志"不传即 false"
			// 与服务端"不传即 true"的缺省相互矛盾
			return callMCPTool("publish_digital_employee", map[string]any{
				"agentUuid":      flagOrFallback(cmd, "assistant", "id"),
				"allowJoinGroup": !noJoin,
			})
		},
	}
	employeePublishCmd.Flags().String("assistant", "", "数字员工 ID (assistantId)")
	employeePublishCmd.Flags().String("id", "", "数字员工 ID (assistant 的别名)")
	employeePublishCmd.Flags().Bool("no-join-group", false, "禁止被拉入群聊 (默认允许)")
	DeclareLeafMetadata(employeePublishCmd, LeafSpec{
		Safety: deapWriteSafety(),
		Contract: deapLeafContract("publish_digital_employee", "deap employee publish",
			"发布数字员工（使当前草稿生效）",
			"草稿已配置完整、要让数字员工正式生效时",
			"配置还没保存时先用 employee save-draft；发布不携带配置",
			"dws deap employee publish --assistant <assistantId>"),
	})

	employeeDeleteCmd := &cobra.Command{
		Use:   "delete",
		Short: "删除数字员工（不可逆）",
		Long: `彻底移除数字员工。

不可逆：服务端会撤销运行态感知、删除本体、清理触发规则。且事务只覆盖数据库，
对外的推送与消息副作用不回滚——若返回失败，不要简单重试，
应先用 employee get 确认它当前是否还存在。`,
		Example: "  dws deap employee delete --assistant <assistantId>",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateRequiredFlagWithAliases(cmd, "assistant", "id"); err != nil {
				return err
			}
			return callMCPTool("delete_digital_employee", map[string]any{
				"agentUuid": flagOrFallback(cmd, "assistant", "id"),
			})
		},
	}
	employeeDeleteCmd.Flags().String("assistant", "", "数字员工 ID (assistantId)")
	employeeDeleteCmd.Flags().String("id", "", "数字员工 ID (assistant 的别名)")
	DeclareLeafMetadata(employeeDeleteCmd, LeafSpec{
		Safety: contract.SafetySpec{
			Effect: "destructive", Risk: "high",
			Confirmation: "user_required", Idempotency: "unknown",
		},
		Contract: deapLeafContract("delete_digital_employee", "deap employee delete",
			"删除数字员工（不可逆）",
			"确认要彻底移除某个数字员工时；不可逆，请先确认 ID 无误",
			"只想让它暂时不响应时应改配置而非删除；删除无法撤销",
			"dws deap employee delete --assistant <assistantId>"),
	})

	employeeCmd.AddCommand(employeeListCmd, employeeGetCmd, employeeCreateCmd,
		employeeSaveDraftCmd, employeePublishCmd, employeeDeleteCmd)

	cmd := &cobra.Command{
		Use:               "deap",
		Short:             "DEAP 数字员工 (生命周期与执行观测)",
		Long:              "管理 DEAP 数字员工并观测其每次执行。详见 `dws deap employee --help` 与 `dws deap run --help`。",
		Args:              cobra.NoArgs,
		TraverseChildren:  true,
		DisableAutoGenTag: true,
		RunE:              func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	cmd.AddCommand(employeeCmd, newDeapRunCommand())
	return cmd
}
