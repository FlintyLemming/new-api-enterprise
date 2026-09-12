# 生产库（pgloader MySQL→PostgreSQL 迁移）遗留对象说明

## 背景

生产 PostgreSQL（8850-new-api 项目的 `newapi` 库）历史上由 MySQL 通过 pgloader 迁移而来，
因此库里存在两套索引/约束：

1. **GORM 标准命名**：`idx_<table>_<column>`、`<table>_<column>_key` 等；
2. **pgloader 遗留**：`idx_<数字ID>_<MySQL索引名>`（如 `idx_16973_idx_users_access_token`、
   `idx_16853_primary`），其中部分 MySQL 唯一键被转成了 PostgreSQL 的 UNIQUE **约束**
   （名字保留 MySQL 索引名，如 `idx_users_access_token`）。

## 风险：GORM >= 1.25.8 的 MigrateColumnUnique

新版 GORM AutoMigrate 会对每一列执行 `MigrateColumnUnique`：当库里某列存在单列 UNIQUE
**约束**、而模型字段用的是 `uniqueIndex`（非裸 `unique`）时，它会执行
`ALTER TABLE ... DROP CONSTRAINT uni_<table>_<column>` —— 不检查存在性、无 IF EXISTS。
pgloader 遗留约束的名字不是 `uni_*`，于是启动直接 FATAL 崩溃循环。

独立唯一索引不触发该逻辑（driver 只查 `information_schema.table_constraints`），
裸 `unique` 标签的字段也不触发（field.Unique=true 时无操作）。

## 2026-09-03 rc.30 上线时的人工修复

升级 gorm 1.25.2 → 1.25.12（随 v1.0.0-rc.30 合入）后首次启动崩溃。已执行：

```sql
-- prefill_groups：上游迁移只认得 idx_prefill_groups_name，遗留索引挡住启动
DROP INDEX idx_16853_uk_prefill_name;   -- 全局唯一，由 uk_prefill_name（部分唯一）替代

-- 以下 UNIQUE 约束与模型 uniqueIndex 语义重复，且命名会导致 GORM DROP CONSTRAINT 崩溃
ALTER TABLE users DROP CONSTRAINT idx_users_access_token;
ALTER TABLE users DROP CONSTRAINT idx_users_aff_code;
ALTER TABLE passkey_credentials DROP CONSTRAINT idx_passkey_credentials_user_id;
ALTER TABLE passkey_credentials DROP CONSTRAINT idx_passkey_credentials_credential_id;
ALTER TABLE custom_oauth_providers DROP CONSTRAINT idx_custom_oauth_providers_slug;
ALTER TABLE subscription_pre_consume_records DROP CONSTRAINT idx_subscription_pre_consume_records_request_id;
ALTER TABLE redemptions DROP CONSTRAINT idx_redemptions_key;
```

删除后 GORM AutoMigrate 自动按模型重建了对应的唯一索引（`idx_users_access_token` 等），
唯一性无间断（删除约束时各列上仍有遗留的唯一索引兜底）。

## 2026-09-03 冗余索引清理

同日（业务低谷期）清理了全部 105 个冗余的 pgloader 遗留索引（单事务执行）：

- 98 个与 GORM 管理索引**定义完全相同**（列、唯一性、谓词、opclass 逐一比对）的重复索引；
- 7 个人工确认功能冗余的索引：`subscription_orders.trade_no`、`top_ups.trade_no`、
  `two_fas.user_id`、`users.username`（均有同列 UNIQUE 约束兜底）、
  `user_oauth_bindings` 的 `user_id`/`provider_id`（分别被 `ux_user_provider`、
  `ux_provider_userid` 唯一索引最左前缀覆盖）、`users.lark_id`（字段已从代码移除）。

**保留**：25 个 `idx_*_primary` 主键约束（非冗余，仅命名特殊，不影响 GORM）。

## 后续升级注意

- 每次同步上游后，若 `go.mod` 里 gorm/driver 版本变动，启动前先用
  `docs/internal/` 之外的临时查询核对：库里新增的单列 UNIQUE 约束是否与模型
  `uniqueIndex` 字段冲突（参考 `model/prefill_group_migration.go` 的检查 SQL）。
- `users.username` 的约束名是 `idx_users_username`（MySQL 时代名字），模型为裸
  `unique` 标签，GORM 不会动它，保持现状即可。
