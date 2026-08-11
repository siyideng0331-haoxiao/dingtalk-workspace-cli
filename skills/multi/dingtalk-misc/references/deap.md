# DEAP 数字员工（deap）

DEAP 是承载**数字员工**（digital employee）的平台。数字员工是一类特殊智能体：有工号、
岗位、直属上级，被拉进群后按配置的响应模式参与对话。

`dws deap` 覆盖两条链路：

| 链路 | 命令组 | 做什么 |
| --- | --- | --- |
| 生命周期 | `deap employee` | 创建 / 读取 / 配置 / 发布 / 删除数字员工本体 |
| 执行观测 | `deap run` | 查某次执行的状态、按来源反查、拉完整 trace |

## 身份与权限

DEAP **不使用 API Key**。服务端用会话里的 orgId + userId 重建用户上下文，权限判定与
DEAP 后台完全一致，因此命令上没有任何身份参数：

- **管理者**（该数字员工的管理员 / 开发者 / 成员）：可见该数字员工的全部执行
- **使用者**：仅可见自己触发的执行
- 元信息（配置详情）仅管理者可读

两者都不满足时返回无权限，不会退化成"查不到数据"的空结果。

## 生命周期：创建到发布

数字员工创建出来是**草稿态**，不会响应任何消息，必须再发布才生效：

```bash
# 1. 创建（草稿态）
dws deap employee create \
  --name "招聘助手小钉" \
  --description "负责简历初筛与面试安排" \
  --org-code HR --org-name "人力资源部" \
  --position-name "招聘专员" \
  --response-mode mention_only

# 2. 补齐人设提示词（发布必填项之一）
dws deap employee save-draft --assistant <assistantId> --prompt "你是一名招聘助手，..."

# 3. 发布，使草稿生效
dws deap employee publish --assistant <assistantId>
```

### 发布前的必填项

发布会校验 7 项，缺任一项都会被拒**且不产生任何副作用**（可放心补齐后重试）：

名称、头像、描述、组织编码、岗位名称、响应模式、人设提示词。

### 响应模式只有两个合法值

| 值 | 含义 |
| --- | --- |
| `mention_only` | 仅被 @ 时响应 |
| `targeted_proactive` | 定向主动响应 |

传其它值会被服务端拒绝。

### save-draft 是全量覆写

**未传的字段会被清空**，不是保留原值。做增量修改必须先读回全量：

```bash
# 正确：先读回全量，再把要改的项一起传回
dws deap employee get --assistant <assistantId>
dws deap employee save-draft --assistant <assistantId> \
  --name "招聘助手小钉" --description "..." --org-code HR --org-name "人力资源部" \
  --position-name "招聘专员" --response-mode mention_only --prompt "新的人设"

# 错误：只传 --prompt 会把名称、岗位、响应模式等全部清空
dws deap employee save-draft --assistant <assistantId> --prompt "新的人设"
```

### 没有"保存并发布"的单命令

服务端的"保存并发布"是两步拼接且**非原子**：保存成功而发布失败时，草稿已落库、发布未生效。
把它做成一个命令会让人误以为原子，所以这里要求显式两步——失败时能准确知道停在哪一步。

### 删除不可逆

`employee delete` 会撤销运行态感知、删除本体、清理触发规则。且事务只覆盖数据库，
对外的推送与消息副作用不回滚。**返回失败时不要简单重试**，先用 `employee get`
确认它当前是否还存在。

## 执行观测：三个容易搞错的点

### 1. 状态查询按场域分两个口径，不可互换

| 场域 | 用什么查 | 为什么 |
| --- | --- | --- |
| 单聊 | `deap run status --task <taskId>` | 单聊走 DEAP 既有链路，服务端生成 taskId 并登记状态，可轮询 |
| 群内 | `deap run executions --messages <messageId>` | 群内走 Runtime V3 入口，taskId 是调用方传进去的、服务端不登记 |

传错口径会**稳定查不到**——不是偶发失败，而是那个口径根本没有数据。

`run executions` 传的必须是**发起本次执行的用户消息 ID**，不能传助手消息 ID：触发源信息
只写在用户消息上，传助手消息 ID 会查到一条缺触发源的记录。

### 2. 反查用 openMessageId，不是 openTaskId

这两个 ID 很容易混：

| ID | 是什么 | 从哪来 |
| --- | --- | --- |
| `openTaskId` | 发送句柄 | `dws chat message send` 的返回值 |
| `openMessageId` | IM 消息业务键 | 消息**投递后**由 IM 生成 |

`deap run resolve` 接受的是 `openMessageId`。手上只有 `openTaskId` 时要先换：

```bash
# 先用发送句柄换取消息 ID
dws chat message query-send-status --open-task-id <openTaskId>
# 再用消息 ID 反查执行
dws deap run resolve --source <openMessageId>
```

这层映射只有发送侧知道（消息投递后才生成），DEAP 不接收也不存储 `openTaskId`。

### 3. trace 比状态更敏感

`deap run trace` 返回完整对话内容，权限比状态查询严：服务端先做两级权限校验再取内容，
无权时直接拒绝且**不会读取内容**。只想知道成功/失败时用 `run status` 或 `run executions`，
不必拉 trace。

## 典型排查路径

```bash
# 场景：群里有人反馈"数字员工回复得不对"，你手上只有那条消息
dws deap run resolve --source <openMessageId>     # → 拿到 runId
dws deap run trace --trace <runId>                # → 看模型与工具调用链路
```

## 其它来源类型

`run resolve` 的 `--source-type` 除默认的 `im_message` 外还支持：

| 值 | 来源 |
| --- | --- |
| `im_message` | 钉钉 IM 消息（默认） |
| `trigger_rule` | 群感知规则 ID |
| `scenario_instance` | 场景实例 ID |

群感知触发的执行不一定由某条消息发起，所以按规则 ID 与场景实例 ID 也各登记了一条映射。

## 与 chat 的边界

- 数字员工的**管理与观测**在 `deap`
- 数字员工的**对话内容本身**仍在 `chat` 侧查看（它就是普通的群 / 单聊消息）
- 钉钉助理（agentType=1）不在本产品范围内，`deap employee list` 查不到它们
