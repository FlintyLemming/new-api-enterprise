# Exchange Key：按用户名 HMAC 使用该用户额度

日期：2026-08-20
状态：待用户确认 spec
分支：当前工作区
部署：`/home/flintylemming/appdata/8850-new-api`（new-api）；8858 Langfuse 栈不改。

## 1. 目标

公司内其他应用也调用这套 new-api。每人一把随机 `sk-<48 位>` 无法在各应用间同步。

内部应用在本地用公司 SECRET 计算 HMAC，请求携带

```text
Authorization: Bearer sk-<username>-<hmac>
```

即可按该用户身份调用 relay（以及只读的 token usage / log-by-key），扣费走该用户现有的订阅额度和钱包余额，与普通 key 同一套 `BillingSession` 偏好（`subscription_first` / `wallet_first` / `wallet_only` / `subscription_only`）。

成功标准：

- 功能关闭或未配 SECRET 时，现网普通令牌行为不变。
- HMAC 正确、用户存在且未封禁时，请求以该用户默认分组记账，不创建 `tokens` 行。
- HMAC 错误、用户不存在、用户被封禁时，响应不区分原因（封禁仍用现网 TokenAuth 的 403，其余 401）。
- 日志可用 `token_name=exchange-key` 筛出这类流量；日志和上下文不保存 HMAC 后缀。

## 2. 不做

- 不在 `tokens` 表自动建 backing token。
- 不按用户或应用做 allowlist；所有已存在且未封禁用户均可（含管理员）。
- 不自动开户；用户名不存在 → 401。
- 不支持 `sk-<username>-<hmac>-<渠道ID>` 指定渠道；管理员指定渠道仍用普通令牌。
- 不做双 SECRET 轮换窗口；换 SECRET 后各应用必须同步改 HMAC。用户改名后旧钥匙失效。
- 不把 SECRET 明文回给前端或写进通用 `GET /api/option`。
- 不改 `relaykit/`（这是网关鉴权，不是上游协议）。
- 不改 8858 Langfuse 部署。

## 3. 钥匙格式与 HMAC

客户端发送（与现网一样，也接受 Anthropic `x-api-key`、Gemini `key` / `x-goog-api-key`、WS `openai-insecure-api-key`，它们已归一成 Bearer）：

```text
sk-<username>-<hmac>
```

- `<username>`：与 `users.username` **完全一致**（区分大小写，允许中间 `-`）。长度 1–20。解析时不 trim。
- `<hmac>`：`HMAC-SHA256(SECRET, username)` 的 32 字节 MAC，编码为 **64 位小写 hex**。比较时先把请求后缀解码成 32 字节，再 `hmac.Equal`；hex 大小写都接受，内部按小写规范输出。
- HMAC 消息：用户名的 UTF-8 字节，不含 `sk-`、不含后缀。SECRET 为 UTF-8 字节。

内部应用示例（规范，不是实现代码）：

```text
hmac_hex = hex(HMAC-SHA256(secret, username))
key      = "sk-" + username + "-" + hmac_hex
```

**从右边解析**（`ParseExchangeKey`）：去掉可选的 `sk-` 后，最后一个 `-` 右侧必须是恰好 64 位 hex；左侧整段为 username。因此 `sk-foo-bar-<64hex>` 的用户名是 `foo-bar`，不会被拆成 `foo`。

不是该形状 → 不当作 exchange key（走普通令牌失败路径）。

普通令牌 key 是 48 位 `[0-9a-zA-Z]`，不含 `-`。管理员 `sk-<48位>-<渠道ID>` 的最后一段是短数字，不是 64 hex。两条路径不冲突。

## 4. 架构

```
Bearer / x-api-key / Gemini key
        │
        ▼
 TokenAuth / TokenAuthReadOnly
        │
        ├─ 现网：第一段当令牌 key → ValidateUserToken / GetTokenByKey
        │
        └─ 未命中 且 形状匹配 且 功能有效
              HMAC 校验 → 按用户名查用户 → 封禁检查
              写入虚拟令牌上下文（无 DB 行）
        │
        ▼
 BillingSession（用户计费偏好：订阅 / 钱包）
        │
        └─ SkipTokenQuota：不预扣、不增减、不回滚 tokens.*
```

功能有效 ≔ `effectiveEnabled && effectiveSecret != ""`。缺一则 exchange 路径不执行。

## 5. 配置

环境变量优先于系统设置。

| 来源 | 启用 | SECRET |
| --- | --- | --- |
| 环境变量 | `EXCHANGE_KEY_ENABLED`（`true`/`false`，未设置则看 option） | `EXCHANGE_KEY_SECRET`（非空则覆盖 option） |
| 系统设置 | option `exchange_key.enabled` | option `exchange_key.secret` |

SECRET 最短 16 字符；后台写入时拒绝更短。环境变量过短则视为未配置并打启动/热更新警告，功能不生效。

专用控制面（与 Langfuse 密钥同一安全模型，不走会漏密钥的通用 option 列表）：

- `GET /api/option/exchange-key`（管理员）返回：

  ```json
  {
    "enabled": true,
    "secret_configured": true,
    "secret_from_env": true,
    "enabled_from_env": false
  }
  ```

  永不返回 SECRET 明文。

- `PUT /api/option/exchange-key` 体：`enabled`、`secret_key`（空=保持已存）、`secret_key_clear`（仅在结果为关闭时允许）。`secret_from_env` 时拒绝改 `secret_key` / clear；`enabled_from_env` 时拒绝改 `enabled`。

通用 `GET /api/option` 不返回 `exchange_key.*`（与 Langfuse 前缀同样过滤）。`UpdateOption` 拒绝这些 key，避免第二套契约。

运行时读取集中在 `setting/exchange_key`：effective config、Parse、HMAC、常量名 `exchange-key`。`middleware/auth.go` 只调用，不内嵌 HMAC。该包保持无 Gin、无 `relaykit` 依赖。

8850 compose 用环境变量锁生产 SECRET；后台只作开关和「已由环境变量配置」只读提示。密钥本身不写进仓库或本 spec。

## 6. 鉴权细节

### 6.1 顺序

`TokenAuth` 保持现有归一化（Bearer / MJ secret / Anthropic / Gemini / WS）。然后：

1. 按现网取第一段，查普通令牌。
2. 命中则完全走现网（含管理员 `parts[1]` 指定渠道）。**不要**再验 HMAC。
3. 未命中（含 `ErrRecordNotFound`）且 `ParseExchangeKey` 成功且功能有效：验 HMAC → 查用户。
4. 否则现网 401。

`TokenAuthReadOnly` 共用同一解析/校验函数，只是不检查令牌过期/耗尽（虚拟令牌也没有这些状态）。

### 6.2 用户查找

按 `username` 精确查（已有 unique index）。不存在或软删 → 与 HMAC 失败相同的 401。查到后走现网 `GetUserCache`：`Status != enabled` → 403（`MsgAuthUserBanned`），与普通令牌一致。

不新增 Redis 用户名缓存；主键点查即可。

### 6.3 虚拟令牌上下文

通过后**不**调用 `SetupContextForToken` 的渠道后缀逻辑。写入：

| 字段 | 值 |
| --- | --- |
| `id` / user cache | 该用户 |
| `token_id` | `0` |
| `token_name` | 固定 `exchange-key` |
| `token_key` | 空（禁止把 HMAC 放进 context / RelayInfo） |
| `token_unlimited_quota` | `true` |
| token group / model limit / IP allowlist / auto groups | 空 / 关 |
| `ContextKeyUsingGroup` | 用户默认分组 |
| `ContextKeyExchangeKey` | `true` |
| `RelayInfo.IsExchangeKey` | `true`（从 context 填入，与 `IsPlayground` 并列） |

`token_id=0` 与现网「无令牌」日志默认值一致；筛选用 `token_name`，不用 token_id。

异步任务 `PrivateData.TokenId` 为 0 时，现网 `taskAdjustTokenQuota` 已直接 return，无需再为任务表加标记。

### 6.4 计费：跳过令牌额度，不跳过用户资金

普通 Unlimited 令牌仍会 `DecreaseTokenQuota` 更新 `tokens.used_quota`。虚拟钥匙没有行，必须显式跳过。

新增 `RelayInfo.AppliesTokenQuota() bool`：`!(IsPlayground || IsExchangeKey)`。下列路径用它替代「只判断 playground」：

- `PreConsumeTokenQuota`
- `postConsumeQuotaWithResult` 的 token 分支
- `BillingSession.preConsume` 令牌预扣与失败回滚
- `BillingSession.Settle` 令牌调整
- `BillingSession.Refund` 令牌退还
- `PreWssConsumeQuota` 里的 `GetTokenByKey`（exchange 时不要查库；令牌额度视为充足）

资金侧（钱包 / 订阅）**不**改。`shouldTrust` 对 Unlimited 令牌的现网语义保留：exchange 在令牌层等同不限额度，订阅路径仍禁止信任旁路。

用户额度不足、无订阅、订阅不足：与普通 key 相同的 403 与文案。

### 6.5 错误与时序

| 情况 | HTTP | 对外 |
| --- | --- | --- |
| 形状不对 / 功能关 / HMAC 错 / 用户不存在 | 401 | 现网 `MsgTokenInvalid` |
| 用户封禁 | 403 | 现网 `MsgAuthUserBanned` |
| 查用户 DB 失败 | 500 | 现网 `MsgDatabaseError` |
| 额度不足 | 403 | 现网 BillingSession 文案 |

响应不说「HMAC 错」或「用户不存在」。HMAC 用 `hmac.Equal`。不要求防时序侧信道到用户名枚举（公司内网 SECRET）；错误 HMAC 仍可跳过用户查询。

## 7. 组件与文件

| 位置 | 职责 |
| --- | --- |
| `setting/exchange_key/` | effective config、Parse、HMAC、常量 `exchange-key` |
| `controller/option_exchange_key.go` | GET/PUT `/api/option/exchange-key` |
| `controller/option.go` | 通用 GET/PUT 排除 `exchange_key.` 前缀 |
| `model/option.go` | 默认 option；加载进 OptionMap |
| `model/user.go` | 按 username 取用户（精确、含 soft-delete 语义与现网 First 一致） |
| `middleware/auth.go` | TokenAuth / TokenAuthReadOnly 接入 |
| `constant/context_key.go` | `ContextKeyExchangeKey` |
| `relay/common/relay_info.go` | `IsExchangeKey`、`AppliesTokenQuota()` |
| `service/quota.go`、`service/billing_session.go` | 跳过令牌额度 |
| `router/api-router.go` | 注册 option 路由（管理员中间件） |
| `web/src/features/system-settings/security/` | 新 section：开关、SECRET 输入（空=不改）、clear、env 锁定提示 |
| `web/src/i18n/` | 经现有 `i18n:sync`，不手改七份 locale |
| `common/init.go` 或 option 加载 | 读 `EXCHANGE_KEY_*` |

前端安全页增加一节即可，不新开顶级菜单。SECRET 输入永不回显；只显示「已配置」或「由环境变量配置」。

## 8. 错误处理与测试

后端（`testify`；保护契约，不测覆盖率）：

1. **Parse**：`foo` + 64 hex；`foo-bar` + 64 hex；后缀非 64 / 非 hex / 无 `-` → 不是 exchange。
2. **HMAC**：正确通过；改 username 或 secret 失败；大小写 hex 视为同一 MAC。
3. **Auth 表测**（httptest + TokenAuth）：有效钥匙 → `id` 对、`token_id==0`、`token_name==exchange-key`、`IsExchangeKey`；功能关 / 错 MAC / 未知用户 → 401 且 body 相同；封禁用户 → 403。
4. **普通令牌回归**：48 位 key 仍命中 token；管理员 `sk-<key>-<channelId>` 仍设 `specific_channel_id`。
5. **配置**：env SECRET 覆盖 option；GET view 无明文；`secret_from_env` 时 PUT secret 被拒。
6. **计费**：exchange 请求扣用户钱包或订阅（按 fixture 偏好），`tokens` 无新行，且不出现 `id=0` 的 token update；普通有限额度令牌仍改 `remain_quota`。
7. **WSS 预扣**：`IsExchangeKey` 时 `PreWssConsumeQuota` 不因 `GetTokenByKey` 失败而 500。

前端：section 在 env 锁定时禁用密钥输入；空 secret 提交不把已配置清掉（与 Langfuse「空=保持」一致）。

不写随机 fuzz、sleep、或只证明函数能跑的测试。

## 9. 部署

- 无表迁移。option 行在首次保存或启动默认值时出现。
- 默认关闭。镜像上线后现网零行为变化。
- 8850：在 `compose.yaml` 增加 `EXCHANGE_KEY_ENABLED` / `EXCHANGE_KEY_SECRET`（SECRET 只放该主机环境，不进 git）。改 env 需重启容器。
- 内部应用：用同一 SECRET 按 §3 拼钥匙；用户必须已在 new-api 开户并有订阅或余额。
- 回滚：去掉 env 并 `enabled=false`，或只关开关。已产生的 `token_name=exchange-key` 日志保留。

## 10. 威胁模型（本期接受的范围）

持有 SECRET 的人可以花**任意未封禁用户**（含管理员）的额度。SECRET 按生产密钥管理：仅 8850 环境变量、后台不回传、最短 16 字符。泄露后立即轮换 SECRET 并让各应用重算 HMAC；无法按用户吊销（v1 无 allowlist）。这是「内部应用免同步 key」换来的明确代价。
