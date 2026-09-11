# 订阅重置卡（Subscription Reset Card）设计

日期：2026-09-11
状态：已确认（数据模型、后端 API 两节经用户逐节确认；前端、错误处理、测试节由用户授权补全）

## 1. 背景与目标

管理员希望给用户发放"重置卡"，用户在页面上自行使用，将某个订阅的当前时段已用额度清零。现有系统中：

- 兑换码（`model/redemption.go`）只能充值钱包余额，与订阅用量无关；
- 订阅用量重置只有两条路径：后台任务按套餐周期自动重置（`service/subscription_reset_task.go`）、管理员手动重置（`AdminResetUserSubscriptionsByPlan` / `AdminResetPlanSubscriptions`）；
- 用户侧没有任何自助重置入口。

本设计新增"重置卡"作为独立领域实体，**不做卡密兑换**（已明确排除），只支持管理员直接发放到用户账户。

## 2. 已确认的需求决策

| 决策点 | 结论 |
|---|---|
| 发放方式 | 仅管理员直接发放给指定用户，无卡密 |
| 作用范围 | 用户使用时自选名下某个有效订阅 |
| 重置语义 | 清零已用量并**重启周期**（`advance_reset_time=true` 语义） |
| 适用套餐 | 所有套餐可用，包括重置周期为 `never` 的套餐 |
| 使用入口 | 订阅页面对单个订阅使用；用户无需挑卡，系统按 FIFO 消耗 |
| 过期 | 卡支持过期时间（0 = 不过期），过期靠查询时过滤，不设清理任务 |
| 回收 | 管理员可禁用未使用的卡；已使用的卡不可禁用 |

## 3. 数据模型与状态机

新表 `subscription_reset_cards`，模型 `model.SubscriptionResetCard`：

| 字段 | 类型 / GORM tag | 说明 |
|---|---|---|
| `id` | int PK | GORM 自增主键 |
| `name` | varchar | 卡名称/备注，发放时填，如"9月补偿卡" |
| `user_id` | int, index | 归属用户，创建即绑定 |
| `status` | int, default:1 | 1=未使用，2=已使用，3=已禁用（常量风格对齐 `common.RedemptionCodeStatus*`） |
| `created_time` | bigint | 发放时间 |
| `used_time` | bigint | 使用时间 |
| `expired_time` | bigint | 过期时间，0=不过期 |
| `used_subscription_id` | int | 核销的订阅 ID，用于审计追溯 |
| `deleted_at` | `gorm.DeletedAt`, index | 软删除 |

状态机：`未使用 → 已使用`（用户核销）；`未使用 → 已禁用`（管理员）。过期不是独立状态，是查询条件（`expired_time != 0 AND expired_time < now`），与兑换码一致。

迁移：仅 `AutoMigrate` 新增一张表，无存量数据变更。须按 AGENTS.md 数据库规则在 SQLite / MySQL / PostgreSQL 三库验证：全新建库 + 从上一版本升级、启动两次证明幂等。

## 4. 后端设计

### 4.1 核销核心（model 层）

`UseSubscriptionResetCard(userId, subscriptionId int)`：

1. 开启事务；
2. 取该用户**最早过期的一张可用卡**：`WHERE user_id=? AND status=1 AND (expired_time=0 OR expired_time>=now) ORDER BY (expired_time=0) ASC, expired_time ASC, id ASC LIMIT 1`，用 `lockForUpdate(tx)` 锁行；无卡 → 报"没有可用的重置卡"；
3. 事务内复核订阅存在、属于该用户、处于有效状态，加载其套餐；
4. CAS 翻卡状态：`UPDATE ... WHERE id=? AND status=1` 写入 `status=2, used_time, used_subscription_id`；`RowsAffected==0` → 报"重置卡已被使用"（对齐 `Redeem()` 的 CAS 模式，SQLite 无行锁时也安全）；
5. 调 `resetUserSubscriptionTx(tx, sub, plan, now, true)` 清零 `AmountUsed` 并重启周期；
6. 提交后 `RecordLog` 用户日志（卡 ID、订阅 ID、套餐名）。

后台任务 `StartSubscriptionQuotaResetTask` 不改动；自动重置与卡核销共用 `resetUserSubscriptionTx`，无冲突。

### 4.2 发放（model 层）

`GrantSubscriptionResetCards(userId, name string, count int, expiredTime int64)`：校验 `1 <= count <= 100`，同事务批量插入，逐张设置 `created_time`。发放和禁用写管理员审计日志，复用 `auditOperatorInfo` 模式（对齐 `controller/subscription.go` 中 `AdminResetUserSubscriptionsByPlan` 的做法）。

### 4.3 API

路由对齐 `router/api-router.go` 现有惯例：用户端挂在 `/api/subscription/self` 下，管理端挂在 `/api/subscription/admin` 下。

**管理端（`AdminAuth`，`/api/subscription/admin/reset_cards` 分组）**

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/subscription/admin/reset_cards/grant` | 发放：`{user_id, name, count, expired_time}` |
| `GET` | `/api/subscription/admin/reset_cards/?p=&page_size=` | 分页列表，ID 倒序 |
| `GET` | `/api/subscription/admin/reset_cards/search?keyword=&status=` | 搜索（卡 ID / 名称前缀 / 用户 ID），状态过滤含"已过期"虚拟态，逻辑对齐 `SearchRedemptions` |
| `POST` | `/api/subscription/admin/reset_cards/disable` | 禁用未使用的卡（支持批量 ID，逐个校验 ≤1000） |
| `DELETE` | `/api/subscription/admin/reset_cards/:id` | 软删除 |

**用户端（`UserAuth`）**

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/subscription/self/reset_cards` | 返回当前用户可用卡数量（未使用且未过期） |
| `POST` | `/api/subscription/self/reset_cards/use` | 核销：`{subscription_id}`，写路径挂 `CriticalRateLimit()`（对齐 `/self` 下既有写接口） |

**错误约定：** 订阅不存在/不属于该用户/已失效 → 400；无可用卡 → 400"没有可用的重置卡"；并发核销失败 → 400"重置卡已被使用"。错误文案走后端 i18n（en/zh）。

## 5. 前端设计

遵循 web/AGENTS.md：先检索复用 `src/components/` 与 `features/redemption-codes`、`features/subscriptions` 的既有组件与模式，文案全部走 i18next。

### 5.1 用户端（钱包页）

`web/src/features/wallet/components/subscription-plans-card.tsx` 渲染用户有效订阅。在每个有效订阅的操作区加"使用重置卡"按钮：

- 卡数量来自 `GET /api/subscription/self/reset_cards`（React Query，独立 queryKey，如 `['reset-cards','self-count']`）；按钮文案带剩余数量，如"使用重置卡（剩 2 张）"；数量为 0 时按钮禁用；
- 点击弹确认框，**复用 `@/components/confirm-dialog`**，文案说明"将清零该订阅当前已用额度并重新开始重置周期"；
- 确认后 `useMutation` 调 `POST /api/subscription/self/reset_cards/use`，成功 toast + `invalidateQueries` 订阅查询与卡数查询；失败经 `handleServerError` 统一提示。

### 5.2 管理端（新页面）

新增 feature `web/src/features/reset-cards/`（`api.ts`、`types.ts`、`constants.ts`、`components/`），结构照抄 `features/redemption-codes`；新路由 `_authenticated/reset-cards/index.tsx`，导航入口放在兑换码/订阅附近（需管理员权限）。

页面能力：

- 列表：`@/components/data-table`，列为名称、归属用户、状态（未使用/已使用/已禁用/已过期徽标）、创建时间、过期时间、使用时间、使用的订阅；
- 工具栏：搜索（关键字 + 状态下拉）、发放按钮；
- 发放对话框：选用户（复用用户选择既有实现，参考 `features/subscriptions` 的发放/绑定类对话框）、名称、数量（1–100）、过期时间（可空=不过期），React Hook Form + Zod；
- 行操作：禁用（未使用态才显示，ConfirmDialog 二次确认）、删除（ConfirmDialog）。

### 5.3 i18n

新增英文源串 key，同步 `bun run i18n:sync` 补齐 zh（fallback）及其他语言文件；常量中的状态文案按 web/AGENTS.md 约定用 `labelKey` + `t()`。

## 6. 错误处理与并发

- 核销并发安全依赖 CAS（`status=1 → 2` 条件更新），不依赖行锁存在；双击/重试/多标签页同时使用只会成功一次；
- 订阅在确认前后状态变化（过期、被管理员作废）：事务内复核，失败整体回滚，卡不被消耗；
- 禁用与核销竞态：禁用更新带 `status=1` 条件，与核销 CAS 互斥，必有一方失败并给出明确错误；
- 软删除只影响管理端展示，已核销记录的用户日志不受影响。

## 7. 测试策略

**后端**（集中一个测试文件，对齐 `model/subscription_reset_test.go` 风格与 AGENTS.md 测试规则，testify require/assert，显式初始化夹具）：

- 发放：数量边界（0、101 拒绝；1、100 通过）、字段写入正确；
- FIFO 选卡：多张卡时先消耗最早过期的、不过期的排最后；
- 核销成功：用量清零、`NextResetTime` 从当前时刻重算（advance 语义）、卡状态与时间字段正确；
- `never` 套餐订阅也可核销，仅清零用量；
- 失败路径：无可用卡、卡已过期、订阅不属于该用户、订阅已失效；
- 并发：两个事务同时核销同一用户只有一张卡时，只有一个成功；
- 禁用：已使用的卡不可禁用；禁用后不可核销。

**前端**（Vitest + React Testing Library，放各模块 `__tests__/`）：

- 订阅页按钮：有卡可点、无卡禁用、点击弹确认框、确认后调用 API 并刷新数据；
- 管理端发放表单：数量边界校验、提交成功关闭并重置。

**数据库验证**：按 AGENTS.md 要求跑三库矩阵（全新 + 升级 + 双次启动幂等），记录版本、命令与结果。

## 8. 明确不做（YAGNI）

- 卡密生成与兑换入口；
- 卡过期清理后台任务（查询时过滤）；
- 用户端选卡（固定 FIFO）；
- 已使用卡的回收/撤销核销；
- 跨用户转赠。
