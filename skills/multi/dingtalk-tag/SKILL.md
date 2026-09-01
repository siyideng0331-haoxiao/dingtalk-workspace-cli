---
name: dingtalk-tag
description: 钉钉数字员工的自然语言创建、查询、修改、发布、删除、能力资源、执行状态与 DSH 接入。Use when 用户说创建或管理数字员工、改人设/岗位/响应模式、发布或询问下线能力、查执行状态或 trace、管理数字员工 Skill/MCP，或把已有数字员工接入本地 DSH。命令前缀：dws dingtalk-tag。
metadata:
  category: product
  requires:
    bins:
      - dws
---

# 数字员工 Skill

执行任何 `dws` 操作前，先完整读取 [`dingtalk-shared`](../dingtalk-shared/SKILL.md)。先用 [dingtalk-tag-index.md](references/dingtalk-tag-index.md) 路由；生命周期读取 [manage.md](references/manage.md)，自然语言创建/恢复/DSH 接入再读 [manage-and-connect.md](references/manage-and-connect.md)；能力资源读取 [capability.md](references/capability.md)；执行排障读取 [run.md](references/run.md)。

## 路由

| 用户意图 | 命令 |
|---|---|
| 创建草稿 / 创建并发布 / 查询 / 修改 / 上线 / 删除 | `dws dingtalk-tag manage ...` |
| 下线 | 当前版本无独立下线命令；明确说明限制，不得用 delete 冒充下线 |
| 把已有、已发布的本地数字员工接入 DSH | `dws dingtalk-tag connect --agent-uuid ... --channel dsh` |
| 创建或查询 Skill / MCP 资源 | `dws dingtalk-tag capability ...` |
| 查一次执行的状态或完整 trace | `dws dingtalk-tag run ...` |

## 自然语言编排硬约束

- `mainProgramType` 仅支持 `open_code`、`local_agent`，`a2a` 暂不支持；不传时由 OpenAPI 按 `open_code` 处理。创建本地数字员工固定使用 `--main-program-type local_agent`。
- 发布前一次性收集名称、描述、头像、部门、岗位、响应模式、Prompt 等缺失信息，避免边执行边追问。
- `save-draft` 是全量覆写。修改前必须读取完整 draft，并保留未修改的 `mainProgramType`、Skill、MCP 和其它字段。
- 同一自然语言请求里的连续写操作只做一次汇总确认；确认后才加 `--yes`。先用 `--dry-run --format json` 展示计划。
- 创建成功后若保存或发布失败，必须返回已创建的 `agentUuid` 和恢复命令；重试禁止再次执行 create。
- “创建并接入 DSH”可顺序执行创建/发布与 connect，但两者是独立事务。connect 绝不创建、修改或发布数字员工。
- 用户可以只创建/管理数字员工，也可以只把已有员工接入 DSH；两条能力互不依赖。
- 所有 ID 统一使用 `agentUuid` / `--agent-uuid`，不得猜测。

## 安全

- `get-dws-auth-code` 的 `dwsAuthCode` 是一次性高敏感凭证，不得复制到对话、日志、文档、argv 或缓存。普通自然语言接入应只调用 `connect`，不要手工拆解换票链路。
- 删除不可逆；修改、发布、删除和 connect 按 Schema 的确认要求执行。
- Channel 的 `reply` / `operator-private` 只供 DSH 机器协议使用，正文只能走受限 stdin；不要为普通用户消息直接调用。
