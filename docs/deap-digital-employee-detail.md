# DEAP 数字员工详情参数

`dws dingtalk-tag manage detail` 保持单一命令，通过 `--type` 选择要读取的配置版本：

```text
dws dingtalk-tag manage detail --assistant-id <assistantId> --type draft
dws dingtalk-tag manage detail --assistant-id <assistantId> --type published
```

`--type` 只接受 `draft` 或 `published`，默认值为 `draft`。CLI 将参数原样映射到
`get_digital_employee_detail` 的 `type` 字段，不增加新的命令或客户端接口。

详情响应沿用已有的字符串字段 `status` 表示当前发布状态：

- `online`：已发布。
- `dev`、`offline`：未发布。

`type` 表示本次读取的配置版本，`status` 表示数字员工当前是否处于已发布状态。

## 变更历史

- 2026-08-31：顶级命令从 `dws deap` 重命名为 `dws dingtalk-tag`。
- 2026-08-14：detail 增加 `--type=draft|published`，默认 `draft`，并明确已有 `status`
  的发布状态语义。原因：用一个参数完成草稿/发布版本选择，避免增加重复命令与返回字段。
