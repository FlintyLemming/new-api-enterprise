# fix/stream-client-close-after-complete

| 项 | 值 |
| -- | -- |
| 分支 | 直接提交到 `internal-custom` |
| 基线 | `internal-custom` @ `d27d5d0e0` |
| 合入 commit | 见 README 生效中的 patch 表 |
| 合入日期 | 2026-11-05 |
| 状态 | `upstream-pending`（未提交） |
| 上游 PR | 未提交 |

## 为什么要这个改动

2026-11-05 起，mucr（user 422，channel 44，`/v1/messages`）约 30% 的流式请求日志里 `stream_status` 是 `end_reason=client_gone`、`end_error="context canceled"`、`status=error`，request_policy 记成 `stop / stream_not_successful`。但同一条日志的 `response_status=completed`，usage、计费都完整，用户那边也没有异常。

原因是 Anthropic 流没有 `[DONE]`，扫描器只有等到上游 EOF 才算正常结束。上游发出 `message_stop` 后，new-api 调用 `MarkCompleted()` 并把它转发给客户端。如果客户端收到后马上断开，而上游 EOF 还没到，`StreamScannerHandler` 主循环会先进入 `c.Request.Context().Done()` 分支，记成 `client_gone`。日志里的 `scanner error: use of closed network connection` 来自 cleanup 关闭上游 body。

## 改动内容

`relay/helper/stream_scanner.go`：客户端断开时，如果 `StreamStatus.ResponseOutcome()` 已经是 `completed`，就按 `eof` 结束（`EndError` 为 nil），否则仍记 `client_gone`。cleanup 照常立即关闭上游 body。

带来的效果：日志里 `stream_status.status=ok`，request_policy 记为 success，`perf_metrics` 计为成功样本。这正是 `ClassifyRelayOutcome` 注释所说的语义：已经正常完成的流在 context 取消后仍然算样本。真正在中途断开的请求（还没收到终止事件）不受影响。

测试：`TestStreamScannerHandler_ClientCloseAfterCompletedIsNormalEnd`。

## 部署影响

无数据库迁移，不影响计费。
