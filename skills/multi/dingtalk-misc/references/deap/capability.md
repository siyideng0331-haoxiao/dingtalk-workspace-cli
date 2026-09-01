# dingtalk-tag capability — 数字员工能力资源

命令前缀 `dws dingtalk-tag capability`。Skill/MCP 命令负责创建和查询能力资源，不会自动把资源关联到数字员工。

## Skill

```text
dws dingtalk-tag capability skill create --agent-uuid <agentUuid> --file ./skill.zip
dws dingtalk-tag capability skill list --agent-uuid <agentUuid> --snapshot draft
dws dingtalk-tag capability skill query --agent-uuid <agentUuid> --skill-id <skillId> --snapshot draft
```

Skill ZIP 最大 50 MiB，必须包含 `SKILL.md`。创建会先上传 ZIP，再调用 Skill Center 创建资源；临时上传地址不会输出或落盘。

## MCP

```text
dws dingtalk-tag capability mcp create --config-file ./mcp.json
dws dingtalk-tag capability mcp list --keywords <关键词> --page 1 --page-size 20
dws dingtalk-tag capability mcp query --mcp-id <mcpId>
```

MCP 敏感配置必须放在本地 JSON 文件，不要直接拼进命令行。查询结果只返回脱敏信息。

## 关联到数字员工

资源创建成功后，使用 `dws dingtalk-tag manage save-draft --skills-file/--mcps-file` 保存关联配置。`save-draft` 是全量覆写操作，执行前必须先查完整 draft，并保留所有仍需配置的字段。
