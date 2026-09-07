# Sub2API Admin Reference

## Environment

```bash
export SUB2API_BASE_URL='https://your-sub2api-host'
export SUB2API_ADMIN_API_KEY='<admin api key>'
# 或者，未配置管理员 API Key 时使用管理员 JWT：
# export SUB2API_JWT='<admin access_token>'
```

后台鉴权优先使用 `SUB2API_ADMIN_API_KEY` 发送 `x-api-key`，未设置时使用 `SUB2API_JWT` 发送 `Authorization: Bearer <jwt>`。如果返回 `INVALID_ADMIN_KEY`，重新生成管理员 API Key；如果使用 JWT，先用管理员邮箱密码登录并从响应的 `data.access_token` 复制 token：

```bash
curl -sS "$SUB2API_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.com","password":"your-password"}'
```

## CLI

以下命令都假设当前目录是这个 skill 目录。

```bash
node scripts/sub2api-admin.js <command>
```

## Accounts

### 只读

```bash
node scripts/sub2api-admin.js accounts list --page-size 20
node scripts/sub2api-admin.js accounts list --search outlook --platform openai --type oauth --status active
node scripts/sub2api-admin.js accounts get 40
node scripts/sub2api-admin.js accounts usage 40
node scripts/sub2api-admin.js accounts stats 40 --days 30
node scripts/sub2api-admin.js accounts today-stats 40
node scripts/sub2api-admin.js accounts batch-today-stats --ids 40,39
node scripts/sub2api-admin.js accounts models 40
node scripts/sub2api-admin.js accounts temp-unschedulable 40
node scripts/sub2api-admin.js accounts antigravity-default-model-mapping
```

`accounts export` 会包含账号凭据和 token，建议写入文件，不要直接刷屏：

```bash
node scripts/sub2api-admin.js accounts export --ids 40,39 --file accounts-export.json
node scripts/sub2api-admin.js accounts export --platform openai --type oauth --include-proxies false --file accounts-export.json
```

### 单账号写入

```bash
node scripts/sub2api-admin.js accounts create --file account.json
node scripts/sub2api-admin.js accounts update 40 --json '{"concurrency":20}'
node scripts/sub2api-admin.js accounts set-status 40 active
node scripts/sub2api-admin.js accounts set-schedulable 40 true
node scripts/sub2api-admin.js accounts clear-error 40
node scripts/sub2api-admin.js accounts clear-rate-limit 40
node scripts/sub2api-admin.js accounts recover-state 40
node scripts/sub2api-admin.js accounts reset-quota 40
node scripts/sub2api-admin.js accounts refresh 40
node scripts/sub2api-admin.js accounts test 40
node scripts/sub2api-admin.js accounts sync-models 40
node scripts/sub2api-admin.js accounts apply-oauth 40 --file credentials.json
node scripts/sub2api-admin.js accounts reset-temp-unschedulable 40
```

### 删除与清理

删除前先列出目标账号名和 ID。

```bash
node scripts/sub2api-admin.js accounts delete 25
node scripts/sub2api-admin.js accounts keep-only --name 'target@example.com'
```

### 批量写入

```bash
node scripts/sub2api-admin.js accounts batch-create --file accounts.json
node scripts/sub2api-admin.js accounts batch-update-credentials --file payload.json
node scripts/sub2api-admin.js accounts bulk-update --ids 40,39 --json '{"concurrency":10,"priority":2}'
node scripts/sub2api-admin.js accounts batch-refresh --ids 40,39
node scripts/sub2api-admin.js accounts batch-clear-error --ids 40,39
```

`bulk-update` 可覆盖页面“批量更新”的字段，payload 由后台表单字段决定，例如 `base_url`、`model_mapping`、`group_ids`、`proxy_id`、`concurrency`、`priority`、`rate_multiplier`、`status`、`compact_mode` 等。更新前先用 `accounts get <id>` 确认字段名。

### 导入

通用后台导入：

```bash
node scripts/sub2api-admin.js accounts import-data --file accounts-export.json
node scripts/sub2api-admin.js accounts import-codex-session --file payload.json
```

CRS 同步：

```bash
node scripts/sub2api-admin.js accounts crs-preview --file payload.json
node scripts/sub2api-admin.js accounts crs-sync --file payload.json
```

旧版 JSON 导入仍可用，会把模板账号的配置复制给导入账号：

```bash
node scripts/sub2api-admin.js accounts import-json \
  --file /path/accounts.json \
  --template-name 'template@example.com' \
  --dry-run
```

复制字段：

- `concurrency`
- `priority`
- `group_ids`
- `credentials.model_mapping`

## Groups And Proxies

```bash
node scripts/sub2api-admin.js groups all
node scripts/sub2api-admin.js proxies all
```

## Managed User API Keys

一步式按精确邮箱确保用户和命名 Key：

```bash
node scripts/sub2api-admin.js users provision-api-key \
  --email hourong@zhihu.com \
  --group-id 2 \
  --concurrency 5 \
  --expires-in-days 365 \
  --idempotency-key '<stable-digest-without-email>'
```

默认从邮箱本地部分生成用户名和 Key 名称，例如 `hourong@zhihu.com` 对应
`hourong-zhishu-client`。也可通过 `--username` 和 `--key-name` 显式指定。响应包含完整 Key，命令只应在
受控终端运行，不要把输出写入共享日志或工单。

如果已经知道用户 ID，也可以只幂等获取或创建指定用户的命名 Key。`Idempotency-Key` 必须是稳定摘要，不能包含
原始邮箱或其他个人信息；响应含完整 `sk-...`，不要打印到共享终端、日志或工单。

```bash
node scripts/sub2api-admin.js api POST /admin/users/123/api-keys/ensure \
  --idempotency-key 'provisioning-request-sha256-v1' \
  --json '{"name":"employee-zhishu-client","group_id":2,"quota":0,"expires_in_days":365,"rate_limit_5h":0,"rate_limit_1d":0,"rate_limit_7d":0,"ip_whitelist":[],"ip_blacklist":[]}'
```

接口拒绝 `custom_key`，同名历史冲突或不可用 Key 不会自动修复。详细协议见
`docs/ADMIN_MANAGED_API_KEYS.md`。

## Redeem Codes

兑换码类型包括 `balance`、`concurrency`、`subscription`、`invitation`。状态常用 `unused`、`used`、`expired`。

### 只读

```bash
node scripts/sub2api-admin.js redeem-codes list --page-size 20
node scripts/sub2api-admin.js redeem-codes list --type balance --status unused --search user@example.com
node scripts/sub2api-admin.js redeem-codes get 123
node scripts/sub2api-admin.js redeem-codes stats
node scripts/sub2api-admin.js redeem-codes export --file redeem-codes.csv
```

### 生成兑换码

```bash
node scripts/sub2api-admin.js redeem-codes generate \
  --json '{"count":1,"type":"balance","value":10}' \
  --idempotency-key "redeem-generate-$(date +%s)"
```

订阅兑换码需要 `group_id` 和非零 `validity_days`：

```bash
node scripts/sub2api-admin.js redeem-codes generate \
  --json '{"count":1,"type":"subscription","value":0,"group_id":2,"validity_days":30}' \
  --idempotency-key "redeem-subscription-$(date +%s)"
```

### 创建并兑换

用于支付回调或人工充值，一步完成创建兑换码并兑换到用户。生产流程必须传稳定的 `--idempotency-key`。

```bash
node scripts/sub2api-admin.js redeem-codes create-and-redeem \
  --json '{"code":"order_123","type":"balance","value":10,"user_id":123,"notes":"manual recharge"}' \
  --idempotency-key order-123
```

### 修改与清理

写入前先 `list` 或 `get` 核对目标 ID。

```bash
node scripts/sub2api-admin.js redeem-codes batch-update --ids 123,124 --json '{"notes":"campaign A"}'
node scripts/sub2api-admin.js redeem-codes expire 123
node scripts/sub2api-admin.js redeem-codes delete 123
node scripts/sub2api-admin.js redeem-codes batch-delete --ids 123,124
```

## Error Rules And TLS Profiles

对应账号页顶部“错误透传规则”和“TLS 指纹模板”。

```bash
node scripts/sub2api-admin.js error-rules list
node scripts/sub2api-admin.js error-rules get 1
node scripts/sub2api-admin.js error-rules create --file rule.json
node scripts/sub2api-admin.js error-rules update 1 --json '{"enabled":true}'
node scripts/sub2api-admin.js error-rules toggle 1 false
node scripts/sub2api-admin.js error-rules delete 1

node scripts/sub2api-admin.js tls-profiles list
node scripts/sub2api-admin.js tls-profiles get 1
node scripts/sub2api-admin.js tls-profiles create --file profile.json
node scripts/sub2api-admin.js tls-profiles update 1 --file profile.json
node scripts/sub2api-admin.js tls-profiles delete 1
```

## Raw Admin API

未封装或新版本后台接口可用 `api` 直通。路径可写 `/admin/...` 或 `/api/v1/admin/...`。

```bash
node scripts/sub2api-admin.js api GET /admin/groups/all
node scripts/sub2api-admin.js api POST /admin/accounts/bulk-update \
  --json '{"account_ids":[40],"concurrency":10}'
```

## Confirmed Admin Endpoints

- `POST /api/v1/admin/provisioning/employee-api-key`
- `GET /api/v1/admin/accounts`
- `GET /api/v1/admin/accounts/:id`
- `POST /api/v1/admin/accounts`
- `PUT /api/v1/admin/accounts/:id`
- `DELETE /api/v1/admin/accounts/:id`
- `POST /api/v1/admin/accounts/check-mixed-channel`
- `GET /api/v1/admin/accounts/:id/usage`
- `GET /api/v1/admin/accounts/:id/stats`
- `GET /api/v1/admin/accounts/:id/today-stats`
- `POST /api/v1/admin/accounts/today-stats/batch`
- `POST /api/v1/admin/accounts/:id/schedulable`
- `POST /api/v1/admin/accounts/:id/test`
- `POST /api/v1/admin/accounts/:id/refresh`
- `POST /api/v1/admin/accounts/:id/apply-oauth-credentials`
- `POST /api/v1/admin/accounts/:id/clear-error`
- `POST /api/v1/admin/accounts/:id/clear-rate-limit`
- `POST /api/v1/admin/accounts/:id/recover-state`
- `POST /api/v1/admin/accounts/:id/reset-quota`
- `GET /api/v1/admin/accounts/:id/temp-unschedulable`
- `DELETE /api/v1/admin/accounts/:id/temp-unschedulable`
- `GET /api/v1/admin/accounts/:id/models`
- `POST /api/v1/admin/accounts/:id/models/sync-upstream`
- `POST /api/v1/admin/accounts/batch`
- `POST /api/v1/admin/accounts/batch-update-credentials`
- `POST /api/v1/admin/accounts/bulk-update`
- `POST /api/v1/admin/accounts/batch-refresh`
- `POST /api/v1/admin/accounts/batch-clear-error`
- `GET /api/v1/admin/accounts/data`
- `POST /api/v1/admin/accounts/data`
- `POST /api/v1/admin/accounts/import/codex-session`
- `POST /api/v1/admin/accounts/sync/crs/preview`
- `POST /api/v1/admin/accounts/sync/crs`
- `GET /api/v1/admin/accounts/antigravity/default-model-mapping`
- `GET /api/v1/admin/groups/all`
- `GET /api/v1/admin/proxies/all`
- `POST /api/v1/admin/users/:id/api-keys/ensure`
- `GET /api/v1/admin/redeem-codes`
- `GET /api/v1/admin/redeem-codes/export`
- `GET /api/v1/admin/redeem-codes/stats`
- `GET /api/v1/admin/redeem-codes/:id`
- `POST /api/v1/admin/redeem-codes/generate`
- `POST /api/v1/admin/redeem-codes/create-and-redeem`
- `POST /api/v1/admin/redeem-codes/batch-update`
- `POST /api/v1/admin/redeem-codes/batch-delete`
- `POST /api/v1/admin/redeem-codes/:id/expire`
- `DELETE /api/v1/admin/redeem-codes/:id`
- `GET /api/v1/admin/error-passthrough-rules`
- `GET /api/v1/admin/error-passthrough-rules/:id`
- `POST /api/v1/admin/error-passthrough-rules`
- `PUT /api/v1/admin/error-passthrough-rules/:id`
- `DELETE /api/v1/admin/error-passthrough-rules/:id`
- `GET /api/v1/admin/tls-fingerprint-profiles`
- `GET /api/v1/admin/tls-fingerprint-profiles/:id`
- `POST /api/v1/admin/tls-fingerprint-profiles`
- `PUT /api/v1/admin/tls-fingerprint-profiles/:id`
- `DELETE /api/v1/admin/tls-fingerprint-profiles/:id`

## Notes

- 线上写入前先只读核对目标集合。
- 导出结果包含敏感凭据，优先使用 `--file`。
- `PUT /admin/accounts/:id` 和 `bulk-update` 接受宽松请求体，字段名不确定时先用 `accounts get` 或后台页面确认。
