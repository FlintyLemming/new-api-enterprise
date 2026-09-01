# feat/exchange-key

| 项 | 值 |
| -- | -- |
| 分支 | `feat/exchange-key` |
| 基线 | `origin/main` @ `ccd535ef8` |
| 合入 commit | 线性合入（无 merge commit），tip `e4d5ea76` |
| 合入日期 | 2026-08-23 |
| 状态 | `internal-only` |
| 上游 PR | 未提交 |

## 为什么要这个改动

公司内多个内部应用都调用这套 new-api，但 new-api 的令牌是每人一把随机 `sk-<48 位>`，无法在各应用间同步分发。这条 patch 让内部应用在本地用公司 SECRET 计算 `HMAC-SHA256(SECRET, username)`，携带 `Authorization: Bearer sk-<username>-<hmac>` 即可按该用户身份调用 relay，扣费走该用户现有订阅额度和钱包余额，不需要在 `tokens` 表里建 backing token。

不开启（未配 SECRET）时现网普通令牌行为不变。

设计文档：`docs/superpowers/designs/2026-08-20-exchange-key-design.md`。

## 改动内容

- `setting/exchange_key/` — 钥匙解析（从右边取最后一个 `-`，右侧 64 位 hex 为 HMAC，左侧整段为用户名）与配置（option 持久化 + 环境变量优先）
- `middleware/auth.go` — TokenAuth 链路识别 exchange-key 形状，HMAC 校验通过后以该用户身份放行，不创建/不查询 `tokens` 行；失败响应不区分原因
- `service/quota.go`、`service/billing_session.go` — exchange-key 请求跳过 token 行额度扣减，只扣用户额度，沿用用户现有 `BillingSession` 偏好
- `controller/option_exchange_key.go` — 专用 option API，SECRET 不回读明文；通用 option 端点拒绝 exchange-key 配置键
- `controller/billing.go`、`controller/log.go`、`controller/token.go` — exchange-key 可查询该用户的账单统计与按 key 的只读日志；`ff25b154` 修复了该路径原先直接 panic 的问题
- `relay/common/relay_info.go`、`model/log.go` — 日志打 `token_name=exchange-key`，上下文与日志不保存 HMAC 后缀
- `web/src/features/system-settings/security/` — 安全设置页的 exchange-key 区块：开关、secret 生成/保留/清除（`1638b13f` 修复保存后 clear 标志未复位）、调用文档与示例
- 测试：`setting/exchange_key/*_test.go`、`middleware/exchange_key_auth_test.go`、`service/exchange_key_quota_test.go`、`controller/*_exchange_key_test.go` 及前端四个测试文件

## 部署影响

- 新增环境变量 `EXCHANGE_KEY_ENABLED`、`EXCHANGE_KEY_SECRET`；用环境变量配置时后台 UI 锁定对应字段（`ErrSecretLockedByEnv` / `ErrEnabledLockedByEnv`）。也可只走后台 option 配置。
- 无数据库迁移。默认关闭，现网行为不变。
- 换 SECRET 后所有内部应用必须同步重算 HMAC；用户改名后旧钥匙立即失效（HMAC 消息是用户名字节）。

## 与上游的冲突风险

`middleware/auth.go` 的 TokenAuth 是上游高频改动区，合并时保留 exchange-key 分支并确认普通令牌路径语义没被上游改动带偏。`controller/option.go`、`model/option.go` 的键守卫是机械冲突，取并集。`service/quota.go` 计费路径上游也常动，重点确认「跳过 token 行扣费」的判断仍挂在正确的结算点上。

2026-09-01 同步 rc.30 实况：上游 #7076 把普通令牌 `-N` 指定渠道的 `specific_channel_id` context key 换成了 `service.GetChannelConstraints(c).AddPin(...)`（`dto.ChannelPin`，`PinSourceToken`），`exchange_key_auth_test.go` 的断言已改为读 `ResolvedPin()`。另外上游把 web 测试运行器换成了 vitest，本条目的四个前端测试文件已从 `node:test` 转为 vitest 导入（`after` → `afterAll`）。

## 验证方式

```bash
go test ./setting/exchange_key ./middleware -run ExchangeKey
go test ./service -run ExchangeKey
go test ./controller -run ExchangeKey
cd web && bun run typecheck
```

手工验证：配置 SECRET 后用 `sk-<username>-<hex(HMAC-SHA256(SECRET, username))>` 调一次 `/v1/chat/completions`，日志出现 `token_name=exchange-key` 且用户额度减少、`tokens` 表无新行；错误 HMAC 与不存在的用户均 401，封禁用户 403。

## 退出条件

内部专用（依赖公司共享 SECRET 的分发模型），不打算提上游。若内部应用全部迁移到各自持有的普通令牌，可关闭开关后删除整条 patch。
