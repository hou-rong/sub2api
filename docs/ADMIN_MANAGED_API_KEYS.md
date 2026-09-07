# 管理员幂等发放员工 API Key

## 一步式员工发放接口

`POST /api/v1/admin/provisioning/employee-api-key` 根据精确邮箱确保普通用户存在，再确保对应的命名 API Key
存在。该接口供 Zhishu 等受信任的服务端编排使用，必须携带管理员凭证和稳定幂等键：

```http
x-api-key: ${SUB2API_ADMIN_API_KEY}
Idempotency-Key: <不含邮箱等原始身份信息的稳定摘要>
Content-Type: application/json
```

示例请求：

```json
{
  "email": "hourong@zhihu.com",
  "group_id": 2,
  "concurrency": 5,
  "rpm_limit": 0,
  "quota": 0,
  "expires_in_days": 365,
  "rate_limit_5h": 0,
  "rate_limit_1d": 0,
  "rate_limit_7d": 0,
  "ip_whitelist": [],
  "ip_blacklist": []
}
```

`email` 按 `lower(trim(email))` 精确匹配。`username` 未提供时使用邮箱本地部分；`key_name` 未提供时使用
`<邮箱本地部分>-zhishu-client`，因此示例会得到 `hourong-zhishu-client`。新用户固定为普通用户，密码由
服务端安全随机生成且不返回，备注固定为 `provisioned by zhishu`，目标分组会写入允许分组并开启公开分组限制。
调用方不能提供 `password`、`role` 或 `custom_key`。

成功响应：

```json
{
  "code": 0,
  "message": "success",
  "data": {
    "user_created": true,
    "api_key_created": true,
    "user": {
      "id": 123,
      "email": "hourong@zhihu.com",
      "username": "hourong",
      "status": "active"
    },
    "api_key": {
      "id": 456,
      "user_id": 123,
      "name": "hourong-zhishu-client",
      "key": "sk-<完整 Key>",
      "status": "active",
      "group_id": 2
    }
  }
}
```

创建相同邮箱发生并发冲突时，服务端会重新执行精确邮箱查询并继续，不会创建重名用户。用户已禁用时返回
`423 USER_INACTIVE`；分组不存在、禁用或用户没有订阅权限时返回 `403 GROUP_NOT_ALLOWED`。如果创建用户后
Key 发放失败，可以使用同一请求重试；已创建的用户会被精确复用。

幂等记录只保存 `user_id`、`api_key_id`、`user_created` 和 `api_key_created`，不保存完整 Key。重放时会重新
读取并校验用户邮箱、用户状态、分组权限和 Key 状态，再返回完整 Key。响应始终包含 `Cache-Control: no-store`
和 `Pragma: no-cache`。审计动作是 `admin.provisioning.employee_api_key.ensure`，审计详情不记录完整 Key。

## 已知用户 ID 的 Key 发放接口

## 接口

`POST /api/v1/admin/users/:id/api-keys/ensure` 供受信任的服务端编排使用。请求必须同时携带：

```http
x-api-key: ${SUB2API_ADMIN_API_KEY}
Idempotency-Key: <不含邮箱等原始身份信息的稳定摘要>
Content-Type: application/json
```

示例请求体：

```json
{
  "name": "employee-zhishu-client",
  "group_id": 2,
  "quota": 0,
  "expires_in_days": 365,
  "rate_limit_5h": 0,
  "rate_limit_1d": 0,
  "rate_limit_7d": 0,
  "ip_whitelist": [],
  "ip_blacklist": []
}
```

接口不接受 `custom_key`。Key 由服务端使用安全随机数生成，成功响应中的 `data.api_key.key` 是完整的
`sk-...` 凭证；调用方不得记录或持久化该响应。响应始终包含 `Cache-Control: no-store` 和
`Pragma: no-cache`。

## 语义

- 用户必须存在且为 `active`。
- 分组必须存在、启用，并且用户有权使用；订阅分组要求有效订阅。
- 同一 `user_id + name` 没有 Key 时创建一把并返回 `created=true`。
- 恰好存在一把可用 Key 时返回原 Key 和 `created=false`。
- 多把同名 Key 返回 `409 API_KEY_NAME_CONFLICT`，不会自动选择或删除。
- 已有 Key 被禁用、过期或额度耗尽时返回 `409 API_KEY_UNUSABLE`。
- 已有 Key 的分组不同返回 `409 API_KEY_CONFIGURATION_CONFLICT`。

相同 `Idempotency-Key` 与相同请求返回同一结果；请求体不同时返回 `409
IDEMPOTENCY_KEY_CONFLICT`。幂等存储不可用时返回 `503`，不会降级创建。幂等记录只保存 Key ID 和
`created` 标记，不保存完整凭证。不同幂等键的并发首次请求仍由 PostgreSQL 事务级 advisory lock
按 `user_id + name` 串行化；取得该锁后，同一事务还会以行锁重新读取用户、分组，以及适用的专属组授权
或有效订阅，授权快照通过后才允许查询或创建 Key。并发禁用用户/分组或撤销既有授权会与该事务串行化，
不会出现接口返回拒绝但 Key 已经落库的状态；历史同名冲突保持失败关闭。

## 审计与运维

成功操作使用审计动作 `admin.users.api_keys.ensure`，只记录管理员、目标用户 ID、Key ID 和是否新建，
不记录完整 Key。监控应覆盖 HTTP 状态、结构化 reason、延迟、幂等冲突、同名冲突和异常调用速率，
不得采集请求头或响应体中的凭证。

本接口复用现有 `api_keys` 和幂等表，不新增 DDL、回填或数据迁移。回滚应用版本不会删除已创建 Key；
调用方应先停用发放入口，再按 Key ID 做受控禁用或轮换，不得按名称批量删除。
