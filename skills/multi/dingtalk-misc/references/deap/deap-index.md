# DEAP 数字员工索引

DEAP 平台数字员工的管理与观测，命令前缀 `dws dingtalk-tag`。按形态分两个子组，安全属性完全不同：

| 主题 | 命令前缀 | 安全属性 | 详见 |
|---|---|---|---|
| 管理态：创建 / 详情 / 列表 / 临时 DWS token / 草稿覆写 / 发布 / 删除 | `dws dingtalk-tag manage` | token 高敏感；其余含高影响写与不可逆删除 | [`manage.md`](./manage.md) |
| 观测态：执行状态 / 执行 trace | `dws dingtalk-tag observe` | 全部只读；trace 含完整对话内容 | [`observe.md`](./observe.md) |

## 意图路由

| 用户说 | 命令 |
|---|---|
| 创建 / 新建数字员工 | `dws dingtalk-tag manage create`（只建草稿，不发布） |
| 查数字员工详情 | `dws dingtalk-tag manage detail` |
| 数字员工列表 / 搜数字员工 | `dws dingtalk-tag manage list` |
| 获取数字员工临时 DWS token | `dws dingtalk-tag manage get-dws-auth-token` |
| 改人设 / 岗位 / 部门 / 头像 | 先 `detail` 再 `dws dingtalk-tag manage save-draft`（**全量覆写**） |
| 发布 / 上线数字员工 | `dws dingtalk-tag manage publish` |
| 删除数字员工 | `dws dingtalk-tag manage delete`（不可逆） |
| 这次执行成功了吗 / 跑完没 / 什么状态 | `dws dingtalk-tag observe run-status` |
| 为什么这么回答 / 看提示词 / 看工具调用 / 完整链路 | `dws dingtalk-tag observe trace` |
| 手上只有 dws 发消息返回的 openTaskId | 先换成 openMessageId，见 [`observe.md`](./observe.md) |

## 全局约束

- identity（corpId / userId）由可信登录态注入，不对 CLI 暴露；不要尝试传 `--org-id` / `--user-id`。
- 固定调用 MCP product/server `deap-dev`，端点跟随当前 MCP 环境自动选择；`DINGTALK_DEAP_DEV_MCP_URL` 仅用于本地调试覆盖。
- 临时 DWS token 只在当前受控调用链内使用，不写入文档、日志、命令历史、缓存或代码库。
- 所有写操作先 `--dry-run` 核对参数，经用户确认后再加 `--yes`。
