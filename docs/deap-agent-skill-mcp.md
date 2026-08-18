# 数字员工 Skill / MCP 命令

`dws dev deap-agent` 将 Skill/MCP 资源管理与数字员工草稿配置分开：资源命令负责创建和查询，`save-draft` 只保存选择、启用状态和配置值。

## Skill

从本地 ZIP 创建：

```bash
dws dev deap-agent skill create \
  --agent-uuid <agentUuid> \
  --file ./my-skill.zip \
  --format json
```

CLI 在本地检查 ZIP 扩展名、压缩包完整性、路径安全、50 MiB 上限、解压规模和 `SKILL.md`。随后使用当前登录身份，把文件流式上传到 OpenAPI `POST /v1.0/assistant/skills`。OpenAPI 负责复用 Studio 上传并调用 Skill Center create/query；CLI 不接收或输出临时签名 URL。

查询命令：

```bash
dws dev deap-agent skill list --agent-uuid <agentUuid> --snapshot draft --format json
dws dev deap-agent skill query --agent-uuid <agentUuid> --skill-id <skillId> --snapshot draft --format json
```

## MCP

敏感配置必须放在本地 JSON 文件中，不要直接拼进命令行：

```bash
dws dev deap-agent mcp create --config-file ./mcp.json --format json
dws dev deap-agent mcp list --keywords 文档 --page 1 --page-size 20 --format json
dws dev deap-agent mcp query --mcp-id <mcpId> --format json
```

CLI 对配置文件和输出执行递归敏感字段保护；服务端 detail 只应返回脱敏元数据和工具列表。

## 草稿配置和详情

`save-draft` 使用 JSON 数组文件传入配置：

```bash
dws dev deap-agent save-draft \
  --agent-uuid <agentUuid> \
  --skills-file ./skills.json \
  --mcps-file ./mcps.json
```

字段未传表示保持该类配置，空数组表示清空，非空数组表示覆写。`detail --type draft` 返回草稿配置，`detail --type published` 仍读取已发布配置；保存草稿不会自动改变线上结果。

## 变更历史

- 2026-08-18：新增 Skill/MCP create/list/query、save-draft Skill/MCP 文件参数和 detail snapshot 参数；Skill create 接入 OpenAPI 流式 multipart 一步创建。原因：复用后台权威链路，同时避免在 MCP JSON、日志和用户输出中传递 ZIP 或临时签名 URL。
