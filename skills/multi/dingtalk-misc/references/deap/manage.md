# dingtalk-tag manage — 数字员工生命周期管理

命令前缀 `dws dingtalk-tag manage`。全部针对数字员工配置本体，含高影响写与不可逆删除。

## create — 创建草稿态数字员工

```
Usage:
  dws dingtalk-tag manage create --name <名称> --description <职责描述> [flags]
Flags:
  --name             必填，同组织内唯一（≤30 Unicode 码点）
  --description      必填，职责描述（≤300 码点）
  --dept-id          归属部门 ID
  --dept-name        归属部门名称
  --icon             头像地址或 OSS objectPath
  --employee-no      工号（≤64 码点）
  --position-name    岗位名称（≤128 码点，发布前必填）
  --supervisor-uid   直属上级钉钉 uid
  --main-program-type 主程序类型（服务端支持的非空字符串）
  --response-mode    mention_only | targeted_proactive | mention_only,targeted_proactive（发布前必填）
  --profile-json     档案 JSON 对象；独立档案 flag 覆盖其同名字段
Example:
  dws dingtalk-tag manage create --name "周报助手" --description "汇总并推送团队周报" --dry-run --format json
```

只建草稿，不会上线。`--position-name` 与 `--response-mode` 是发布的前置条件，可在此处给或后续用 `save-draft` 补。

`--profile-json` 只接收 `employeeNo`、`positionName`、`directSupervisorUid`、`mainProgramType`、`responseMode` 五个字段。`mainProgramType` 由服务端判断是否支持，CLI 仅校验并透传非空字符串；`responseMode` 支持单值，也支持用英文逗号分隔的双值组合，CLI 会规范化为 `mention_only,targeted_proactive`。独立的 `--main-program-type` 会覆盖 `profile-json.mainProgramType`。

## detail / list — 查询

```
Usage:
  dws dingtalk-tag manage detail --agent-uuid <agentUuid>
  dws dingtalk-tag manage list [--keyword <关键词>] [--page 1] [--page-size 20]
Example:
  dws dingtalk-tag manage detail --agent-uuid <agentUuid> --format json
  dws dingtalk-tag manage list --keyword "周报" --format json
```

`--keyword` 按名称、岗位或工号模糊匹配。`--page` / `--page-size` 均不得小于 1。

## get-dws-auth-code — 获取临时 DWS 授权码

```
Usage:
  dws dingtalk-tag manage get-dws-auth-code --agent-uuid <agentUuid> [--client-id <appId>]
Flags:
  --agent-uuid   必填，数字员工 ID
  --client-id    可选，用于授权的应用 ID；不传时由服务端选择默认应用
```

固定调用 MCP 工具 `get_dws_auth_code`。服务端响应结构为：

```json
{
  "data": {
    "dwsClientId": "<clientId>",
    "uid": 123456789,
    "dwsAuthCode": "<sensitive-short-lived-auth-code>",
    "staffId": "<staffId>",
    "orgId": 123456789
  },
  "success": true,
  "errorCode": null,
  "errorMsg": null
}
```

`data.dwsAuthCode` 是数字员工的临时 DWS 授权码，`dwsClientId` 是配套应用 ID，`uid`、`staffId`、`orgId` 是授权身份上下文。CLI 原样输出服务端 envelope，不解析、不缓存这些字段。`dwsAuthCode` 是高敏感短期凭证，只在当前受控调用链内使用；不得写入文档、日志、命令历史、缓存或代码库。

## save-draft — 全量覆写草稿

```
Usage:
  dws dingtalk-tag manage save-draft --agent-uuid <agentUuid> [flags]
Flags:
  --agent-uuid       必填
  --prompt           人设 / System Prompt（≤5000 码点）
  其余基础与档案字段同 create（name / description / icon / dept-* / employee-no /
  position-name / supervisor-uid / main-program-type / response-mode / profile-json）
```

**这是全量覆写，不是增量 patch：未传字段会被清空。**

正确的增量修改姿势：

1. 先 `dws dingtalk-tag manage detail` 查出当前完整配置
2. 把**全部仍需保留的字段**（包括详情返回的 `mainProgramType`）连同要改的字段一并带上
3. 整体提交

只传要改的那一个字段会把其它字段全部清空，且这个后果在 `--dry-run` 的参数预览里看不出来（预览只显示你传了什么，不显示"没传的会被清掉"）。

特别注意 `--icon`：不要把 `detail` 返回的临时 `iconUrl` 直接回填持久化——那是带时效的临时地址。

写操作，需用户确认：先 `--dry-run`，确认后加 `--yes`。

## publish — 发布

```
Usage:
  dws dingtalk-tag manage publish --agent-uuid <agentUuid> [--allow-join-group]
```

**不携带任何配置**，只发布当前已保存的草稿。名称、头像、描述、组织、岗位、响应模式、人设任一缺失都会返回 `INVALID_PARAM`——先 `detail` 确认齐全再发。

`--allow-join-group` 是可选布尔，控制是否允许加入群聊。

## delete — 删除

```
Usage:
  dws dingtalk-tag manage delete --agent-uuid <agentUuid>
```

**不可逆，且可能有跨系统副作用。** 失败时不要盲目重试，先 `detail` 确认该数字员工是否仍存在——重试可能作用在已被部分删除的状态上。

## 硬约束

- `save-draft` 是全量覆写；不先 `detail` 就提交会静默清空未传字段。
- `save-draft` / `publish` / `delete` 必须 `--dry-run` + 用户确认后再 `--yes`。
- 不要传 `--org-id` / `--user-id`：identity 由可信登录态注入，不对 CLI 暴露。

## 跨产品协作

- 查上级或成员的 `uid` / `userId`：切换独立 skill [`dingtalk-contact`](../../../dingtalk-contact/SKILL.md)，或用 `dws aisearch person --query "姓名"`。
- 发布后要看执行情况：见 [`run.md`](./run.md)。
