# 订阅重置卡 Implementation Plan — Verification（Task 11）

> 本文件是 `docs/superpowers/plans/2026-09-11-subscription-reset-card.md` 的拆分文件。先读主计划与 spec。本任务在 backend（Task 1–5）与 frontend（Task 6–10）全部完成后执行。

**本部分目标：** 全量测试回归 + 按 AGENTS.md 数据库规则完成 SQLite / MySQL / PostgreSQL 三库验证矩阵，并把版本、命令、结果记录到交付说明。

---

### Task 11: 全量回归 + 三库数据库验证

**Files:**
- 无新增文件；若发现 bug，回到对应 Task 修复。

**Interfaces:**
- Consumes: Task 1–10 全部产出。
- Produces: 最终交付说明中的数据库验证记录（版本、命令、结果）。

- [ ] **Step 1: 后端全量测试与静态检查**

```bash
go build ./...
go test ./model/ -v -count=1 2>&1 | tail -20
go test ./controller/ ./common/ ./i18n/ -count=1
go vet ./...
gofmt -l model/ controller/ common/ i18n/ router/
```

Expected: 构建通过；`model` 包全部测试 PASS（重点确认 `TestSubscriptionResetCard*`、`TestAdminReset*`、`TestRedeem*` 系列）；`go vet` 与 `gofmt` 无输出。`relaykit` 不受影响（本特性不触碰），但保险起见跑一次 `cd relaykit && GOWORK=off go build ./...`。

- [ ] **Step 2: 前端全量校验**

```bash
cd web
bun run typecheck
bun run lint
bun run test
bun run format:check
bun run build
```

Expected: typecheck 无错误；lint 无 error（既有 warning 不归本任务）；全部 vitest 用例 PASS；format 检查通过（所有新文件含 AGPL 头）；生产构建成功且 `routeTree.gen.ts` 已包含 reset-cards 路由。

- [ ] **Step 3: 三库验证 — SQLite（全新建库 + 幂等）**

```bash
# 全新建库
rm -f /tmp/reset-card-verify.db
SQL_DSN=/tmp/reset-card-verify.db go run . --port 3001 &
sleep 5 && curl -s localhost:3001/api/status | head -c 200; echo
kill %1
# 二次启动证明 AutoMigrate 幂等
SQL_DSN=/tmp/reset-card-verify.db go run . --port 3001 &
sleep 5 && curl -s localhost:3001/api/status | head -c 200; echo
kill %1
sqlite3 /tmp/reset-card-verify.db ".schema subscription_reset_cards"
```

Expected: 两次启动均无 AutoMigrate 报错；schema 含 `id`、`name`、`user_id`（带索引）、`status`（default 1）、`created_time`、`used_time`、`expired_time`、`used_subscription_id`、`deleted_at`（带索引）。

- [ ] **Step 4: 三库验证 — MySQL 与 PostgreSQL（全新 + 升级 + 幂等）**

对 MySQL（>= 5.7.8）与 PostgreSQL（>= 9.6，建议用当前支持的最低与主流版本各一）分别执行：

```bash
# 以 PostgreSQL 为例（MySQL 同理，换 DSN）
docker run -d --name pg-reset-verify -e POSTGRES_PASSWORD=postgres -p 55432:5432 postgres:<version>
createdb -h localhost -p 55432 -U postgres newapi_fresh

# 1) 全新建库
SQL_DSN="postgresql://postgres:postgres@localhost:55432/newapi_fresh" go run . --port 3002 &
sleep 8 && curl -s localhost:3002/api/status | head -c 200; echo
kill %1
# 二次启动（幂等）
SQL_DSN="postgresql://postgres:postgres@localhost:55432/newapi_fresh" go run . --port 3002 &
sleep 8 && curl -s localhost:3002/api/status | head -c 200; echo
kill %1

# 2) 从上一版本升级：用升级前的代码（git stash 本特性或切到 main）建库，再切回本特性代码启动
git worktree add /tmp/new-api-prev main
(cd /tmp/new-api-prev && SQL_DSN="postgresql://postgres:postgres@localhost:55432/newapi_upgrade" go run . --port 3003 &)
sleep 8 && kill %1
createdb -h localhost -p 55432 -U postgres newapi_upgrade 2>/dev/null || true
SQL_DSN="postgresql://postgres:postgres@localhost:55432/newapi_upgrade" go run . --port 3003 &
sleep 8 && curl -s localhost:3003/api/status | head -c 200; echo
kill %1

# 3) 表结构核对（PG 注意 pgloader 遗留约束，见 memory：升级库须核对 UNIQUE 约束无冲突）
psql -h localhost -p 55432 -U postgres newapi_fresh -c '\d subscription_reset_cards'
docker rm -f pg-reset-verify
```

Expected（每库三项全过）：
1. 全新建库启动无报错，`subscription_reset_cards` 表创建成功；
2. 升级路径：上一版本建好的库在本特性代码下启动，`subscription_reset_cards` 被补上，既有表/数据不动；
3. 启动两次均成功（AutoMigrate 幂等，无重复 ALTER/报错）。

- [ ] **Step 5: 三库上跑通一次端到端核销（PG/MySQL 行锁路径）**

SQLite 下行锁被 `lockForUpdate` 跳过，行锁路径必须在真实 PG/MySQL 上演习：

```bash
# 服务起在 PG（或 MySQL）上，管理员 session 下：
curl -X POST localhost:3002/api/subscription/admin/reset_cards/grant \
  -H 'Cookie: session=<admin>' -H 'Content-Type: application/json' \
  -d '{"user_id":<uid>,"name":"验证卡","count":1,"expired_time":0}'
# 该用户 session 下：
curl localhost:3002/api/subscription/self/reset_cards -H 'Cookie: session=<user>'
# 期望 {"success":true,"data":{"count":1}}
curl -X POST localhost:3002/api/subscription/self/reset_cards/use \
  -H 'Cookie: session=<user>' -H 'Content-Type: application/json' \
  -d '{"subscription_id":<sub_id>}'
# 期望 success:true；再次调用期望 success:false 且 message 为"没有可用的重置卡"（或英文）
```

Expected: 发放、计数、核销、重复核销拒绝全链路符合预期；数据库中卡行 `status=2`、`used_time`、`used_subscription_id` 正确，订阅行 `amount_used=0`、`last_reset_time`/`next_reset_time` 刷新。

- [ ] **Step 6: 记录验证结果并交付**

在最终交付说明（或 PR 描述）中记录：每库的**确切版本、使用的命令、三项验证（全新/升级/幂等）结果、端到端核销结果**。若某库无法验证（环境缺失），明确报告为 blocker，不得声称数据库兼容完成。

（按 AGENTS.md「Documentation files」规则，不新增 `docs/` 下的记录文件，除非用户明确要求。）
