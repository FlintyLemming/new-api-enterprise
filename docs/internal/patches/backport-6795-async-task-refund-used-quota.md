# backport-6795-async-task-refund-used-quota

| 项 | 值 |
| -- | -- |
| 分支 | 无（上游 PR 提交 `58d4e9bd` 直接进入 `internal-custom` 历史） |
| 基线 | `origin/main` @ 2026-08-12 前后（当时 tip 即 `58d4e9bd`，后被上游 rebase 移出 main） |
| 合入 commit | `58d4e9bd` |
| 合入日期 | 2026-08-13 |
| 状态 | `upstream-pending` |
| 上游 PR | [#6795](https://github.com/QuantumNous/new-api/pull/6795)（作者 wans10） |

## 为什么要这个改动

异步任务（Midjourney/Suno 等）退款时只退了任务记录，没有同步减少用户的 `used_quota`，用户额度被退款后仍显示已用，长期累积导致可用额度越来越少。这是上游 PR #6795 的修复，内部在合并前先行携带。

## 改动内容

- `service/task_billing.go`、`service/midjourney.go`、`relay/mjproxy_handler.go` — 任务退款路径同步扣减用户 `used_quota`
- `service/quota.go`、`model/user.go` — 用户额度更新的配套调整
- `service/task_billing_test.go`、`model/user_update_test.go` — 退款与额度联动回归测试

## 部署影响

无。

## 与上游的冲突风险

这条就是上游 PR 本身，同步上游时先检查 `origin/main` 是否已含等价修复（PR 可能被 squash/rebase 成别的 SHA，按语义而不是 SHA 核对）：

```bash
git log --oneline origin/main -- service/task_billing.go
```

上游收编后直接取上游实现，本条归档。注意 `service/task_billing.go` 上游重构频繁（已有 `bc14c18f` 退款逻辑重写），核对语义时以「退款同时减 used_quota」为准。

## 验证方式

```bash
go test ./service -run TaskBilling
go test ./model -run UserUpdate
```

手工验证：触发一次异步任务退款，用户 `used_quota` 与额度同步回滚。

## 退出条件

上游 main 出现等价修复即移除内部副本，条目移入已归档。
