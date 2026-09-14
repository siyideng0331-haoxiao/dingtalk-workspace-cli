---
category: Fixed
---

- **数字员工服务端绑定** — 修复 bind、rebind 与 unbind MCP 参数被错误包进 DTO 名称对象的问题，按工具 Schema 将业务字段直接发送到 `tools/call.arguments` 顶层。
- **数字员工绑定回执** — bind/rebind 支持服务端顶层 `runtimeBindingId` 返回值并兼容旧版 `data` 字符串，避免服务端已绑定成功但客户端误留 pending；冲突或无效 ID 仍按未知结果保护。
