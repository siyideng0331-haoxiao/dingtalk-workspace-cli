# 数字员工 DWS 登录

预发验收可通过一个命令把数字员工 DWS 身份写入目标 Agent 的配置目录：

```bash
DWS_CONFIG_DIR=/path/to/agent-a/.dws \
  dws deep manage login-dws --assistant-id <Assistant_ID>
```

`deap` 是产品的规范命令名，`deep` 是兼容入口。下面两条命令等价：

```bash
dws deap manage login-dws --assistant-id <Assistant_ID>
dws deep manage login-dws --assistant-id <Assistant_ID>
```

## 预发准备

登录命令复用 Local Runner 的 `deap_openapi_url` 配置，并从当前 `mcp_url` 的 `/cli/clientId`
自动取得 AuthCode exchange 所需的 DWS OAuth ClientID。为目标 Agent 准备独立目录并指向预发：

```bash
export DWS_CONFIG_DIR=/path/to/agent-a/.dws
mkdir -p "$DWS_CONFIG_DIR"
printf '%s\n' 'https://pre-mcp.dingtalk.com' > "$DWS_CONFIG_DIR/mcp_url"
printf '%s\n' 'https://pre-deap-open-api.dingtalk.com' > "$DWS_CONFIG_DIR/deap_openapi_url"
```

执行登录前，当前配置目录的默认身份应能取得操作人的 DWS OAuth token。可以先检查：

```bash
DWS_CONFIG_DIR=/path/to/agent-a/.dws dws auth status
```

登录命令会临时忽略当前 `DWS_PROFILE`，以默认操作人身份向 OpenAPI 申请数字员工授权；服务端按
操作人的创建/管理权限查询数字员工。拿到可信 `corpId+uid` 后，CLI 只在 AuthCode exchange 和
持久化期间切到数字员工的精确 profile。

命令成功后输出类似：

```text
[OK] 数字员工 DWS 登录成功
Assistant ID:   assistant-001
Profile:        ding-corp:987654
企业 ID:        ding-corp
用户:           987654
```

输出不会包含 AuthCode、access token 或 refresh token。

## 使用已有 AuthCode 调试

如果已经从预发 MCP 调试页取得可信的 `AuthCode + CorpID + UID`，可以跳过 OpenAPI 获取步骤，直接
验证后半段 exchange 和本地持久化：

```bash
DWS_CONFIG_DIR=/path/to/agent-a/.dws \
DWS_PROFILE=ding-corp:987654 \
dws deap manage login-dws \
  --auth-code '<one-time-auth-code>' \
  --corp-id 'ding-corp' \
  --uid '987654'
```

`--assistant-id` 与这组三个参数互斥。AuthCode 是一次性敏感凭证；CLI 的性能报告会对参数值脱敏，
但 shell history 和系统进程列表仍可能记录命令行，因此该入口只用于短期预发调试，日常使用仍推荐
`--assistant-id` 一键链路。

## 启动 Agent

使用命令输出的 `Profile` 绑定 OpenCode、Codex 或其他 Harness 进程：

```bash
DWS_CONFIG_DIR=/path/to/agent-a/.dws \
DWS_PROFILE=ding-corp:987654 \
opencode
```

所有由该进程继承环境并启动的 `dws` 子进程都会选择数字员工精确身份。另一个 Agent 使用自己的
`DWS_CONFIG_DIR` 和 `DWS_PROFILE`，两者可以同时运行。

## 隔离语义

- `DWS_CONFIG_DIR` 隔离 profile 元数据、当前选择和 OpenAPI 环境配置。
- `DWS_PROFILE` 绑定当前进程及其子进程使用的精确身份，推荐固定为 `corpId:uid`。
- Token 的精确 Keychain 槽位按 `corpId+uid` 区分。同一组织中的操作人与数字员工使用不同精确槽位。
- 数字员工登录不会撤销或覆盖操作人的精确 token，也不会持久切换操作人的 current profile。
- 默认链路由 CLI 完成 grant 获取和 exchange；只有预发调试时才使用已有 AuthCode 注入入口。
- 精确 `DWS_PROFILE=corpId:uid` 登录和刷新只更新对应身份槽位，不改写机器全局或组织兼容槽位。

可以分别验证两个身份：

```bash
DWS_CONFIG_DIR=/path/to/agent-a/.dws dws auth status
DWS_CONFIG_DIR=/path/to/agent-a/.dws DWS_PROFILE=ding-corp:987654 dws auth status
```

## 变更历史

- 2026-08-23：`login-dws` 在 exchange 前从当前 MCP `/cli/clientId` 自动取得并注入 OAuth ClientID。
  原因：AuthCode 兑换必须使用签发它的 DWS OAuth 应用身份，调用方和 OpenAPI 响应不应感知或传递
  ClientID。
- 2026-08-22：新增 `--auth-code + --corp-id + --uid` 调试切面，并将精确 profile 的登录/刷新限制为
  只写精确身份槽位。原因：复用相同 exchange 流程验收已有 AuthCode，同时避免同组织数字员工覆盖
  操作人的全局兼容登录态。
- 2026-08-22：新增 `deap/deep manage login-dws --assistant-id` 一键登录链路。原因：同一台电脑上的
  多个 Local Runner Agent 需要分别继承关联数字员工的 DWS 身份，同时保留操作人的日常 DWS 身份。
