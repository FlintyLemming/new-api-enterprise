# 订阅重置卡（Subscription Reset Card）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 新增"订阅重置卡"领域实体：管理员直接发放给用户，用户在钱包页自助将名下某个有效订阅的当前周期已用额度清零并重启重置周期（`advance_reset_time=true` 语义）。

**Architecture:** 后端新增 `model.SubscriptionResetCard`（GORM 新表，仅 AutoMigrate），核销走"事务内 FIFO 选卡 + `lockForUpdate` + CAS 状态翻转 + `resetUserSubscriptionTx`"模式（对齐 `Redeem()`）；控制器挂在既有 `/api/subscription/self` 与 `/api/subscription/admin` 路由组下，错误文案走后端 i18n。前端新增 `web/src/features/reset-cards/` 管理页（结构照抄 `features/redemption-codes`），钱包页订阅卡片加"使用重置卡"按钮（React Query + `ConfirmDialog`）。

**Tech Stack:** Go 1.25.1 / Gin / GORM v2（SQLite + MySQL + PostgreSQL 三库兼容）；React 19 / TypeScript / TanStack Router+Query / React Hook Form + Zod / Tailwind；前端包管理用 Bun。

**Spec:** `docs/superpowers/specs/2026-09-11-subscription-reset-card-design.md`（计划按此设计论证，执行时需一并阅读）

## Global Constraints

以下约束逐字来自 spec 与 AGENTS.md，所有任务隐含遵守：

- 发放方式：仅管理员直接发放给指定用户，无卡密兑换入口。
- 重置语义：清零已用量并重启周期（`resetUserSubscriptionTx(tx, sub, plan, now, true)`）。
- 卡状态：`1=未使用，2=已使用，3=已禁用`（新常量 `common.SubscriptionResetCardStatus*`，**不要复用** `RedemptionCodeStatus*` 的取值顺序）。
- 过期：`expired_time = 0` 表示不过期；过期只是查询过滤条件，不设清理任务、不是独立状态。
- 单次发放数量 `1 <= count <= 100`；批量禁用 ID 数 `1 <= len(ids) <= 1000`。
- 用户端核销 FIFO：用户不可选卡，系统消耗"最早过期"的可用卡。
- 已使用的卡不可禁用；禁用与核销竞态靠 `status=1` 条件更新互斥。
- 后台任务 `StartSubscriptionQuotaResetTask` 不改动。
- 用户端错误文案（无可用卡 / 卡已被使用 / 订阅无效）走后端 i18n（en + zh-CN）。
- 数据库：所有 schema/ORM 代码必须兼容 SQLite / MySQL >= 5.7.8 / PostgreSQL >= 9.6；行锁统一用 `model/locking.go` 的 `lockForUpdate(tx)`，禁止 `tx.Set("gorm:query_option", ...)`。
- 后端 JSON 操作一律走 `common/json.go` 包装函数；本特性基本不涉及手写 JSON 编解码。
- 后端测试集中在 `model/subscription_reset_card_test.go` 一个文件，testify `require`/`assert`，显式夹具初始化。
- 前端：先检索复用既有组件（`@/components/confirm-dialog`、`@/components/data-table`、`@/components/ui/*`）；所有文案走 `t('English key')`；改 TS/TSX 后必须 `bun run typecheck` 与 `bun run lint` 无 error。
- YAGNI（明确不做）：卡密生成/兑换、过期清理任务、用户选卡、撤销核销、跨用户转赠。

## 计划文件拆分

| 文件 | 内容 | 任务 |
|---|---|---|
| `2026-09-11-subscription-reset-card-backend.md` | 数据模型、核销/发放核心、管理端 model 函数、控制器与路由 | Task 1–5 |
| `2026-09-11-subscription-reset-card-frontend.md` | reset-cards 管理页、发放对话框、钱包页用户按钮、前端 i18n | Task 6–10 |
| `2026-09-11-subscription-reset-card-verification.md` | 全量测试、typecheck/lint、三库数据库验证矩阵 | Task 11 |

## 任务索引（按依赖顺序执行）

- **Task 1**（backend）：`SubscriptionResetCard` 实体、状态常量、哨兵错误、AutoMigrate、测试夹具注册
- **Task 2**（backend）：`GrantSubscriptionResetCards`（发放）
- **Task 3**（backend）：`UseSubscriptionResetCard`（核销核心，FIFO + CAS + 事务内复核订阅）
- **Task 4**（backend）：管理端 model 函数（列表/搜索/禁用/软删/可用数）
- **Task 5**（backend）：控制器、路由、后端 i18n key、审计模板
- **Task 6**（frontend）：`features/reset-cards/` 基础（types/api/constants/lib + static-keys）
- **Task 7**（frontend）：发放对话框（TDD，含用户搜索选择）
- **Task 8**（frontend）：管理页表格、行操作、页面、路由、侧边栏
- **Task 9**（frontend）：钱包页"使用重置卡"按钮（TDD）+ 订阅卡片集成
- **Task 10**（frontend）：前端 locale 文件补 key + `bun run i18n:sync`
- **Task 11**（verification）：全量测试 + 三库验证矩阵并记录结果

## 关键既有代码锚点（实施前必读）

- `model/redemption.go` — CAS 兑换模式（`Redeem()`）、`SearchRedemptions` 过期虚拟态过滤、`BatchDeleteRedemptions` 批量校验风格
- `model/subscription.go:1012` — `resetUserSubscriptionTx(tx, sub, plan, now, advanceResetTime)`；`model/subscription.go:389` — `getSubscriptionPlanByIdTx(tx, id)`；`model/subscription.go:29` — `SubscriptionResetNever = "never"`
- `model/locking.go:20` — `lockForUpdate(tx)`
- `model/db_time.go:7` — `GetDBTimestamp()`
- `model/task_cas_test.go` — `TestMain`（SQLite :memory: 夹具）与 `truncateTables(t)`
- `model/subscription_reset_test.go` — 订阅重置测试风格
- `controller/redemption.go` — 管理端 CRUD handler 与 `common.GetPageQuery` 分页
- `controller/audit.go` — `recordManageAudit` / `recordManageAuditFor` / `auditOperatorInfo` 与 `auditContentTemplates`
- `router/api-router.go:169-196` — `/api/subscription` 路由组现状
- `web/src/features/redemption-codes/` — 前端管理页完整镜像对象
- `web/src/features/wallet/components/subscription-plans-card.tsx` — 用户侧订阅卡片（用 useState/useEffect 而非 React Query；按钮集成点在每个订阅行的渲染块内）
- `web/src/features/subscriptions/api.ts` — `getSelfSubscriptionFull` 等用户侧订阅 API 的归属文件
