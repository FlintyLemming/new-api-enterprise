# feat/new-user-default-subscription

| 项 | 值 |
| -- | -- |
| 分支 | `feat/new-user-default-subscription` |
| 基线 | `main` @ `48a6c87f9`（`v1.0.0-rc.49`） |
| 合入 commit | `3ecec49fc` |
| 合入日期 | `2026-11-17` |
| 状态 | `internal-only` |
| 上游 PR | 未提交 |

设计与实施计划：[设计](../../superpowers/specs/2026-11-17-new-user-default-subscription-design.md)、[计划](../../superpowers/plans/2026-11-17-new-user-default-subscription.md)。

## 为什么要这个改动

8850 只开放 SSO（OIDC）登录，账号在首次登录时自动创建，但上游没有"新账号自动获得订阅"的能力，新用户拿不到「5小时150M套餐」（plan 2）。此前靠外部脚本在建号后几秒内调管理接口发放，脚本停掉后新用户（如 588、589 号）就没有订阅，只能管理员逐个手动发。改成内置功能后不再需要单独维护脚本。

## 改动内容

- `setting/operation_setting/subscription_setting.go` — 新增 `subscription_setting.new_user_plan_id`（int，0 = 关闭），注册到 `config.GlobalConfig`，存 `options` 表，随 `SyncOptions` 同步到其他节点。
- `model/subscription.go`、`model/user.go` — `User.Insert` / `User.InsertWithTx` 在 `tx.Create(user)` 之后同事务调用 `grantNewUserSubscriptionTx`，用现成的 `CreateUserSubscriptionFromPlanTx` 发放，来源记 `new_user`。覆盖所有建号路径：OIDC/OAuth（含自定义 provider）、密码注册、微信、管理员后台新建；外层事务回滚时订阅一起回滚。不检查套餐 `enabled`（plan 2 是停用状态）。套餐不存在时跳过并记服务端日志、照常注册；合规条款未确认时不发放；其他错误回滚建号。套餐带升级分组时同步更新内存里的 `user.Group`。
- `controller/subscription.go`、`router/api-router.go` — `GET` / `PUT /api/subscription/admin/settings`（`AdminAuth`）。PUT 要求已确认合规条款，拒绝缺字段、负数、非数字和不存在的套餐，通过 `UpdateOptionsBulk` 写库（会返回数据库错误；`UpdateOption` 会吞掉错误）。
- `middleware/access_token_routes.go` — 访问令牌规则 `billing:read` / `billing:write`。
- `middleware/audit.go`、`controller/audit.go`、`web/src/features/usage-logs/lib/format.ts` — 管理审计 `subscription.settings_update`，参数为新旧套餐 ID。
- `web/src/features/subscriptions/` — 订阅管理页「新建套餐」左侧加「设置」按钮（合规未确认时禁用），弹窗选择默认套餐，停用套餐标注"已停用"，"不发放"即 0；设置读到之前保存按钮禁用，避免读取失败时误存 0。
- `web/src/i18n/locales/*.json` — 七种语言新增 6 个键；`common/log_text.zh-CN.json` 新增跳过发放日志的中文。

## 部署影响

- 无数据库迁移；新增 option 键 `subscription_setting.new_user_plan_id`，默认 0（不发放），升级后行为不变。
- 上线后需在「订阅管理 → 设置」里选择「5小时150M套餐」才会生效；已有用户不补发（如 588 号仍需手动补发）。
- 合规条款必须是当前版本已确认状态，否则保存设置会被拒绝、建号也不发放。上游以后提升 `CurrentComplianceTermsVersion` 时，需要重新确认合规条款，否则自动发放会静默停止（跳过时没有日志）。
- 安全：以后若开放密码注册，每个新账号都会拿到订阅，存在批量注册薅额度的风险；当前只开放 SSO。

## 与上游的冲突风险

- `model/user.go` 的 `Insert` / `InsertWithTx`：上游常改建号流程，合并时保留 `tx.Create(user)` 之后的 `grantNewUserSubscriptionTx` 调用，并确认上游没有新增绕过这两个函数的建号路径。
- `controller/subscription.go`、`router/api-router.go`、`middleware/access_token_routes.go`、`middleware/audit.go`、`controller/audit.go`：上游订阅管理接口一改就可能相邻冲突，保留内部两条路由与规则即可。
- `web/src/i18n/locales/*.json`：按 `localeCompare` 排序插入，和上游或其他内部 patch 在相邻位置加键时会冲突，两边都保留并保持排序。首次合入 `internal-custom` 时就与订阅重置卡、Exchange Key 的键相邻冲突过。
- `web/src/features/subscriptions/components/subscriptions-primary-buttons.tsx`、`subscriptions-dialogs.tsx`：上游改订阅页按钮或弹窗挂载时需要重新加回。

## 验证方式

```bash
# 新增测试（SQLite 默认跑；MySQL/PostgreSQL 需 loopback 临时实例）
TEST_MYSQL_DSN='root:<pwd>@tcp(127.0.0.1:<port>)/mysql?charset=utf8mb4&parseTime=True&loc=Local' \
TEST_POSTGRES_DSN='postgres://postgres:<pwd>@127.0.0.1:<port>/postgres?sslmode=disable' \
go test ./controller/ -run TestNewUserDefaultSubscription -count=1 -v
go test ./router/ -run TestAccessTokenRouteRulesCoverEveryDashboardRoute -count=1
cd web && bun run test src/features/subscriptions/components/__tests__/subscription-settings-dialog.test.tsx
```

2026-11-17 记录：

| 范围 | 结果 |
| -- | -- |
| 分支上 `go build ./...`、`go vet ./...`、`go test ./...` | 通过（47 个包） |
| 合入后 `go build ./...`、`go vet ./...`、`go test ./...` | 通过（52 个包） |
| 新测试数据库矩阵（分支上与合入后各一次，本机临时容器，未连生产库） | SQLite 3.50.4、MySQL 8.4.5、MySQL 5.7.44、PostgreSQL 15.17、PostgreSQL 9.6.24 全部通过 |
| 前端 `bun run test` | 分支上 205 个文件 / 2502 个用例；合入后 219 个文件 / 2624 个用例，均通过 |
| 前端类型检查 `tsgo -b`（本机 Bun 1.3.14 无 `bun check`） | 通过 |
| 前端 `oxlint`（改动文件）、`bun run i18n:sync` | 无 error；七种语言 missingCount=0 |
| 前端 `bun run build` | 通过，约 67.5 MB |

反向确认：去掉发放调用后 `insert_grants_plan` 失败；发放改走 `DB` 而非事务后 `outer_rollback_discards_grant` 在 PostgreSQL 上失败；PUT 改回 `UpdateOption` 时 `database_write_failure_is_reported` 失败；去掉按钮禁用条件、失败时关闭弹窗后对应前端用例失败；删掉 GET 路由的令牌规则后 `TestAccessTokenRouteRulesCoverEveryDashboardRoute` 失败。合入前另有一次独立代码审查（结论"修复后可合并"，8 处问题已修，2 处小问题延后：合规未确认时跳过发放无日志；审计日志页面不显示新旧套餐 ID）。

OWASP：本改动在注册流程上加了副作用，并新增一个管理设置接口。对照 [Authentication Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html)、[Authorization Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Authorization_Cheat_Sheet.html)、[Logging Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Logging_Cheat_Sheet.html)：授权全部在服务端（`AdminAuth` + 访问令牌 scope），前端禁用只是提示；设置变更写管理审计，不含凭据；未改动认证、会话或凭据处理本身。未解决的缺口：开放密码注册时没有防批量注册的限制（见部署影响）。

## 退出条件

上游提供等价的"新用户默认订阅/注册赠送订阅"功能后，迁移设置值并移除内部实现。外部发放脚本不再需要，也不应恢复，否则会与内置发放重复发放。
