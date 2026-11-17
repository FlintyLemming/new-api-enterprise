# 新用户默认订阅 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 管理员在订阅管理页设置一个套餐，此后所有新建账号在建号事务内自动获得该套餐订阅。

**Architecture:** 新增注册到 `config.GlobalConfig` 的 `subscription_setting.new_user_plan_id`；`User.Insert` / `User.InsertWithTx` 在 `tx.Create(user)` 后同事务调用 `CreateUserSubscriptionFromPlanTx`；订阅管理接口组新增 GET/PUT `/settings`；前端在「新建套餐」左侧加「设置」按钮与弹窗。

**Tech Stack:** Go 1.26 / Gin / GORM v2；React 19 + TanStack Query + react-i18next + Vitest/RTL；Bun 1.3.14（本机，`bun check` 不可用，用 `./node_modules/.bin/tsgo -b` 代替）。

**Spec:** `docs/superpowers/specs/2026-11-17-new-user-default-subscription-design.md`

## Global Constraints

- 设置键：`subscription_setting.new_user_plan_id`，int，0 = 关闭。
- 订阅来源字符串：`new_user`。
- 不检查套餐 `enabled`；不给已有用户补发。
- 合规条款未确认（`!operation_setting.IsPaymentComplianceConfirmed()`）时：PUT 拒绝、建号不发放。
- 套餐不存在：建号不发放、`common.SysError` 记一行，注册照常；其余发放错误使建号事务回滚。
- JSON 一律走 `common.Marshal/Unmarshal`；新 Go 测试用 testify `require`/`assert`；Go 文件 `gofmt`。
- 后端只新增一个测试文件：`controller/subscription_new_user_test.go`。
- 面板消息为英文源文本，新键加到 `web/src/i18n/locales/` 全部七个文件。
- 前端测试放 `web/src/features/subscriptions/components/__tests__/`。
- 数据库验证必须跑真实 SQLite、MySQL、PostgreSQL（本机 loopback 临时容器），不连生产库。

## Review Focus

1. 套餐带 `UpgradeGroup`：新用户 `users.group` 与内存 `user.Group` 都应是升级后的分组（OAuth 登录随后用内存对象建会话）。→ Task 1 用例 `upgrade_group`。
2. 管理员后台新建用户走 `InsertWithTx` 且外层还有授权写入：发放必须落在同一事务。→ Task 1 用例经 `InsertWithTx` 包在 `model.DB.Transaction` 里。
3. 停用套餐（plan 2 现状）：应能设置、应能发放。→ Task 1 与 Task 2 用例均用 `Enabled=false` 套餐。
4. PUT 缺字段 / 负数 / 非数字：返回 `Invalid parameters`，已存设置不变。→ Task 2 用例。
5. 设置重启后仍生效：值写入 `options` 表。→ Task 2 断言 `options` 行值。

---

### Task 1: 设置项与建号事务内发放

**Files:**
- Create: `setting/operation_setting/subscription_setting.go`
- Modify: `model/subscription.go`（新增来源常量与发放方法）
- Modify: `model/user.go`（`Insert`、`InsertWithTx` 中 `tx.Create(user)` 之后调用）
- Test: `controller/subscription_new_user_test.go`（新建）

**Interfaces:**
- Produces:
  - `operation_setting.SubscriptionSetting{ NewUserPlanId int \`json:"new_user_plan_id"\` }`，`config.GlobalConfig.Register("subscription_setting", &subscriptionSetting)`
  - `func operation_setting.GetSubscriptionSetting() *SubscriptionSetting`
  - `const operation_setting.SubscriptionNewUserPlanIdOptionKey = "subscription_setting.new_user_plan_id"`
  - `const model.SubscriptionSourceNewUser = "new_user"`
  - `func (user *User) grantNewUserSubscriptionTx(tx *gorm.DB) error`（未导出，领域概念：新用户默认订阅）

- [ ] **Step 1: 写失败测试** `TestNewUserDefaultSubscriptionGrant`，按方言矩阵 `sqlite` / `mysql`（`TEST_MYSQL_DSN`）/ `postgres`（`TEST_POSTGRES_DSN`）跑，DSN 为空时 `t.Skip`。fixture：复用同包 `newAuditTestDatabase(t, dialect, dsn)`；替换并在 Cleanup 恢复 `model.DB`、`model.LOG_DB`、`common.SetDatabaseTypes`、`common.RedisEnabled=false`、`common.OptionMap`（设为新 map）、`operation_setting.GetPaymentSetting()` 的 `ComplianceConfirmed=true` / `ComplianceTermsVersion="v1"`、`GetSubscriptionSetting().NewUserPlanId`；`AutoMigrate(&model.User{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.Option{})`，日志库 `AutoMigrate(&model.Log{}, &model.AuditLog{})`；`t.Logf` 打印数据库版本。套餐：`{Title:"5h150M", DurationUnit:"year", DurationValue:100, TotalAmount:150000000, QuotaResetPeriod:"custom", QuotaResetCustomSeconds:18000, Enabled:false}`（注意 `Enabled` 带 `default:true`，建后用 `Update("enabled", false)` 落库）。子用例：
  - `insert_grants_plan`：设置为该套餐 id，`(&model.User{Username:"nu-a", Password:"password123", Role:common.RoleCommonUser}).Insert(0)`；断言该用户恰 1 条 `user_subscriptions`：`PlanId` 相等、`Source=="new_user"`、`Status=="active"`、`AmountTotal==150000000`、`NextResetTime-StartTime==18000`。
  - `insert_with_tx_grants_plan`：同上，但 `model.DB.Transaction(func(tx){ return user.InsertWithTx(tx, 0) })`。
  - `disabled_setting_grants_nothing`：设置 0，Insert 后订阅数 0。
  - `missing_plan_skips_grant`：设置 999999，Insert 返回 nil、用户行存在、订阅数 0。
  - `compliance_unconfirmed_skips_grant`：`ComplianceConfirmed=false`，订阅数 0。
  - `upgrade_group`：另建套餐 `UpgradeGroup:"vip"`，Insert 后 `user.Group=="vip"` 且 DB 中 `users.group=="vip"`，订阅 `PrevUserGroup` 为插入前分组。

- [ ] **Step 2: 运行确认失败**
  Run: `go test ./controller/ -run TestNewUserDefaultSubscriptionGrant -count=1`
  Expected: 编译失败（`GetSubscriptionSetting` 未定义）

- [ ] **Step 3: 实现**
  - `subscription_setting.go`：结构体、默认值 0、`init` 注册、getter、键常量（照 `checkin_setting.go` 写法）。
  - `grantNewUserSubscriptionTx`：读 `GetSubscriptionSetting().NewUserPlanId`；`<= 0` 或合规未确认返回 nil；`getSubscriptionPlanByIdTx(tx, id)` 返回 `gorm.ErrRecordNotFound` 时 `common.SysError(common.LogText("new user default subscription plan %d not found, skipped for user %s", id, user.Username))` 并返回 nil；其他错误返回；`CreateUserSubscriptionFromPlanTx(tx, user.Id, plan, SubscriptionSourceNewUser)`，成功且 `sub.UpgradeGroup != ""` 时 `user.Group = sub.UpgradeGroup`。
  - `Insert` / `InsertWithTx`：`tx.Create(user)` 出错直接返回，成功后 `return user.grantNewUserSubscriptionTx(tx)`。
  - 新服务端日志行若要中文，追加到 `common/log_text.zh-CN.json`（可选，与现有行格式一致）。

- [ ] **Step 4: 运行确认通过**
  Run: `go test ./controller/ -run TestNewUserDefaultSubscriptionGrant -count=1 -v`
  Expected: sqlite 子用例全 PASS，mysql/postgres SKIP（矩阵在 Task 4 跑）

- [ ] **Step 5: 回归** `go test ./model/ ./controller/ -count=1` 全 PASS

- [ ] **Step 6: Commit** `feat(subscription): grant a default subscription plan to new users`

### Task 2: 设置接口

**Files:**
- Modify: `controller/subscription.go`（两个 handler + 请求结构）
- Modify: `router/api-router.go`（`subscriptionAdminRoute` 组内加两条路由）
- Modify: `middleware/access_token_routes.go`（订阅管理块内加两条规则）
- Modify: `middleware/audit.go`（`"PUT /api/subscription/admin/settings": "subscription.settings_update"`）
- Modify: `controller/audit.go`（模板 `"subscription.settings_update": "Set the new user default subscription plan from ${old_plan_id} to ${new_plan_id}"`）
- Test: `controller/subscription_new_user_test.go`

**Interfaces:**
- Consumes: Task 1 的 `GetSubscriptionSetting()`、`SubscriptionNewUserPlanIdOptionKey`
- Produces:
  - `GET /api/subscription/admin/settings` → `{success:true, data:{new_user_plan_id:int}}`，令牌规则 `billing:read`
  - `PUT /api/subscription/admin/settings`，body `{new_user_plan_id:int}` → `{success:true, data:{new_user_plan_id:int}}`，令牌规则 `billing:write`
  - `type AdminSubscriptionSettingsRequest struct { NewUserPlanId *int \`json:"new_user_plan_id"\` }`
  - `func AdminGetSubscriptionSettings(c *gin.Context)`、`func AdminUpdateSubscriptionSettings(c *gin.Context)`

- [ ] **Step 1: 写失败测试** `TestNewUserDefaultSubscriptionSettingsAPI`（同文件、同 fixture，可抽出两测试共用的 fixture 函数），用 `gin.CreateTestContext` + `httptest` 直接调 handler，`c.Set("id", 1)`：
  - PUT `{"new_user_plan_id": <停用套餐 id>}` → `success:true`；`GetSubscriptionSetting().NewUserPlanId` 等于该 id；`options` 表 `subscription_setting.new_user_plan_id` 行值为该 id 字符串；随后 GET 返回同值。
  - PUT `{"new_user_plan_id": 0}` → success，设置为 0。
  - PUT `{}`、`{"new_user_plan_id": -1}`、`{"new_user_plan_id": "x"}` → `success:false`、message `Invalid parameters`，设置不变。
  - PUT 不存在 id → `success:false`、message `Subscription plan does not exist`，设置不变。
  - 合规未确认 → PUT `success:false`，设置不变。

- [ ] **Step 2: 运行确认失败** `go test ./controller/ -run TestNewUserDefaultSubscriptionSettingsAPI -count=1` → 编译失败

- [ ] **Step 3: 实现** handler：PUT 先 `requirePaymentCompliance`；`ShouldBindJSON` 失败或字段为 nil 或 `< 0` → `ApiErrorT(c, "Invalid parameters")`；`> 0` 时 `model.GetSubscriptionPlanById`，`errors.Is(err, gorm.ErrRecordNotFound)` → `ApiErrorT(c, "Subscription plan does not exist")`，其他错误 `ApiError`；`model.UpdateOption(key, strconv.Itoa(id))`；`recordManageAudit(c, "subscription.settings_update", {"old_plan_id", "new_plan_id"})`；返回新值。路由、令牌规则、审计映射与模板按 Files 列表加。

- [ ] **Step 4: 运行确认通过**，并跑 `go test ./router/ ./middleware/ ./controller/ -count=1`（`TestAccessTokenRouteRulesCoverEveryDashboardRoute` 必须 PASS）

- [ ] **Step 5: Commit** `feat(subscription): add admin settings API for the new user default plan`

### Task 3: 前端设置按钮与弹窗

**Files:**
- Modify: `web/src/features/subscriptions/api.ts`（`getSubscriptionSettings()`、`updateSubscriptionSettings(data)`）
- Modify: `web/src/features/subscriptions/types.ts`（`SubscriptionSettings { new_user_plan_id: number }`；`SubscriptionsDialogType` 加 `'settings'`）
- Create: `web/src/features/subscriptions/components/dialogs/subscription-settings-dialog.tsx`
- Modify: `web/src/features/subscriptions/components/subscriptions-primary-buttons.tsx`（「新建套餐」左侧加 `variant='outline'` 按钮，图标 `Settings`（lucide），文案 `t('Settings')`，`disabled={!complianceConfirmed}`，`onClick={() => setOpen('settings')}`）
- Modify: `web/src/features/subscriptions/components/subscriptions-dialogs.tsx`（挂载弹窗）
- Modify: `web/src/features/usage-logs/lib/format.ts`（`AUDIT_TEMPLATES` 加 `'subscription.settings_update': 'Updated subscription settings'`）
- Modify: `web/src/i18n/locales/{en,zh,zh-TW,fr,ru,ja,vi}.json`
- Test: `web/src/features/subscriptions/components/__tests__/subscription-settings-dialog.test.tsx`

**Interfaces:**
- Consumes: Task 2 的 GET/PUT 契约；现有 `getAdminPlans()` 与查询键 `['admin-subscription-plans', refreshTrigger]`；`useSubscriptions()` 的 `open/setOpen/refreshTrigger`
- Produces: `export function SubscriptionSettingsDialog()`

- [ ] **Step 1: 写失败测试**（`vi.spyOn(api, 'get'|'put')`，QueryClientProvider + Toaster，`SubscriptionsProvider` 包裹并在其中渲染一个点击 `setOpen('settings')` 的触发器，或直接渲染 `SubscriptionsPrimaryButtons` + `SubscriptionsDialogs`；`/api/option/` 的 GET 返回合规已确认）：
  - `opening settings shows the saved default plan`：GET settings 返回 `{new_user_plan_id: 2}`，plans 返回 id 1 启用、id 2 停用；点「Settings」后 `getByRole('combobox', {name: 'New user default plan'})` 的值为 `'2'`，停用套餐选项文本含 `Disabled`。
  - `saving a selection sends the chosen plan id and closes the dialog`：选 `'1'`，点「Save」，`put` 被以 `('/api/subscription/admin/settings', {new_user_plan_id: 1})` 调用，弹窗消失。
  - `choosing do not grant saves zero`：选 `'0'` 保存，`put` 以 `{new_user_plan_id: 0}` 调用。
  - `a failed save keeps the dialog open`：`put` 返回 `{success:false, message:'Subscription plan does not exist'}`，弹窗仍在。
- [ ] **Step 2: 运行确认失败** `cd web && ~/.bun/bin/bun run test src/features/subscriptions/components/__tests__/subscription-settings-dialog.test.tsx`
- [ ] **Step 3: 实现** 弹窗用 `@/components/dialog` 的 `Dialog`（`title={t('Subscription settings')}`、footer 放「Cancel」「Save」`Button`），`NativeSelect`（`aria-label={t('New user default plan')}`）：选项 `0` → `t('Do not grant')`，其后每个套餐 `title`，停用者追加 ` (${t('Disabled')})`；当前值不在列表时显示 `#id`。说明文字 `t('New accounts, including those created by SSO sign-in, automatically receive this plan. Existing users are not affected.')`。读取用 `useQuery`（键 `['admin-subscription-settings']`，`enabled: open==='settings'`，`requireServerSuccess`），保存用 `useMutation`：成功 `toast.success(t('Saved successfully'))`、invalidate 设置查询、`setOpen(null)`；失败 `handleServerError`，弹窗保持打开。
- [ ] **Step 4: i18n** 新键：`Subscription settings`、`New user default plan`、`Do not grant`、上面说明句、`Updated subscription settings`、后端 `Subscription plan does not exist`；七个语言文件都加译文（zh：订阅设置 / 新用户默认订阅 / 不发放 / 新创建的账号（含 SSO 登录自动创建）会自动获得该套餐，已有用户不受影响。/ 更新了订阅设置 / 订阅套餐不存在）。
- [ ] **Step 5: 运行确认通过**：上面的测试文件 PASS；`./node_modules/.bin/tsgo -b` 无错；`~/.bun/bin/bun run lint` 涉及文件无新增 error；`~/.bun/bin/bun run i18n:sync` 七种语言 missingCount=0（还原生成的 `_reports` 改动）。
- [ ] **Step 6: Commit** `feat(web): add subscription settings dialog for the new user default plan`

### Task 4: 全量与数据库矩阵验证

- [ ] **Step 1:** 本机起 loopback 临时容器 `newapi-nuds-{mysql84,mysql57,pg15,pg96}`（`-p 127.0.0.1::3306` / `::5432`），分别设置 `TEST_MYSQL_DSN` / `TEST_POSTGRES_DSN`，运行 `go test ./controller/ -run 'TestNewUserDefaultSubscription' -count=1 -v`；四个组合加 SQLite 全部 PASS，记录日志里的数据库版本。结束后删除容器。
- [ ] **Step 2:** `go build ./... && go vet ./...`；`go test ./... -count=1` 全 PASS。
- [ ] **Step 3:** `cd web && ~/.bun/bin/bun run test`、`~/.bun/bin/bun run build` 通过。
- [ ] **Step 4:** 反向确认：临时把 `grantNewUserSubscriptionTx` 调用注释掉，`insert_grants_plan` 失败；恢复。

### Task 5: 合入 internal-custom 与台账（需用户确认后执行）

主检出 `docs/internal/README.md` 有用户未提交的改动，动台账前先问用户如何处理。

- [ ] 主检出 `git merge --no-ff feat/new-user-default-subscription -m "Merge branch 'feat/new-user-default-subscription' into internal-custom"`，冲突按内部改动保留。
- [ ] 合并后在 `internal-custom` 上重跑 `go build ./...` 与两个新测试、前端新测试。
- [ ] 从 `docs/internal/patches/_template.md` 建 `feat-new-user-default-subscription.md`，台账表加一行，单独 `docs(internal): ...` 提交。
- [ ] 打 tag、构建镜像、上线 8850 不在本计划内，另行确认。
