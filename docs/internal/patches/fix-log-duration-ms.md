# fix/log-duration-ms

| 项 | 值 |
| -- | -- |
| 分支 | `fix/log-duration-ms` |
| 基线 | `internal-custom` @ `7ddc7866` |
| 合入 commit | `0da33748`（merge；内容早前已以 `d535b112` cherry-pick 进入，merge 只补血统） |
| 合入日期 | 2026-09-01 |
| 状态 | `upstream-pending` |
| 上游 PR | 未提交 |

## 2026-09-09 rc.36 核对

继续保留，上游 `use_time` 仍为秒且未提供毫秒字段。适配 `*model.LogOther`：通过 `SetPublic("duration_ms", ...)` 添加耗时，保留上游对 admin/root/audit 元数据的隔离；`RecordErrorLog` 使用上游 `channelError.ChannelId` 来源。

上游将普通移动端卡片抽到 `common-log-mobile-card.tsx`，在新组件接入毫秒耗时和 TPS；旧 `usage-logs-mobile-card.tsx` 已与上游一致，不保留已删除的布局。新增组件测试覆盖 500ms 短请求显示 0.5s / 200 t/s，以及旧秒数日志回退。上游原组件该测试失败，适配后通过。后续冲突应逐行为整合，不能整文件覆盖上游。验证见 [同步记录](../sync-rc36-2026-09-09.md)。

## 为什么要这个改动

日志的 `use_time` 列只存截断后的整秒（历史契约，保留兼容），而首 token 时间（`other.frt`）已是毫秒。总耗时被显示精度卡在秒级，短请求（几百毫秒）在日志 UI 里要么显示 0 要么无从分辨。这条 patch 让总耗时也以毫秒落库并显示。

## 改动内容

- `model/log.go` — `RecordConsumeLog` / `RecordErrorLog` 的 `UseTimeSeconds` 参数改为 `UseTimeMillis`；毫秒值写入 `other.duration_ms`，`use_time` 列仍由 ms/1000 派生（整秒契约不变）；0 表示未知（task/MJ 计费路径），不写 `duration_ms` 键
- `controller/relay.go`、`controller/channel-test.go`、`service/quota.go`、`service/text_quota.go`、`service/violation_fee.go` — 各调用点改用 `UnixMilli` 计时，顺带消除了 `Unix()` 秒截断带来的最多 1 秒偏差
- `web/src/features/usage-logs/` — 新增 `resolveDurationSeconds`：优先读 `other.duration_ms`，旧日志回退 `use_time`；接入 timing 单元格、移动端卡片、详情弹窗和 tokens/秒 计算
- `model/log_duration_test.go`、`web/.../lib/__tests__/duration.test.ts` — 钉住 consume/error 日志的 `duration_ms` 契约与前端回退行为

## 部署影响

无数据库迁移（`other` 是 JSON 文本列，新增键不改 schema）。旧日志没有 `duration_ms`，前端自动回退到 `use_time` 显示。

## 与上游的冲突风险

`model/log.go` 的 `RecordConsumeLog` 签名变了，上游若在其它路径新增调用点，合并时会编译失败——这是好事，直接按新签名传毫秒即可。`service/quota.go` 计费结算区上游常改，确认计时起点没被上游重构挪走。前端 usage-logs 目录随上游布局更新，仅移植毫秒耗时与兼容回退行为。

2026-09-01 同步 rc.30 实况：上游重构了移动端日志卡片（`usage-logs-mobile-card.tsx` 抽出 `task-mobile-layout`），合并取 import 并集即可，`resolveDurationSeconds` 回退逻辑位置不变；`duration.test.ts` 已随上游测试运行器切换从 `node:test` 转为 vitest 导入。

## 验证方式

```bash
go test ./model -run Duration
cd web && bun run typecheck && bun run test -- duration
```

手工验证：发一次短请求，日志详情里总耗时显示毫秒级数值（如 0.42s），`other.duration_ms` 有值而 `use_time` 为 0。

## 退出条件

上游把 `use_time` 改成毫秒或提供等价字段后，删掉内部实现与前端回退逻辑，条目移入已归档。
