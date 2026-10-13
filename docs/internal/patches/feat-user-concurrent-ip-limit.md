# feature/user-concurrent-ip-limit

| 项 | 值 |
| -- | -- |
| 分支 | `feature/user-concurrent-ip-limit` |
| 基线 | `origin/main` @ `ccd535ef8` |
| 合入 commit | 线性合入（无 merge commit），tip `7ddc7866` |
| 合入日期 | 2026-08-24 |
| 状态 | `upstream-pending` |
| 上游 PR | 未提交 |

## 2026-10-13 rc.44 核对

继续保留。上游 rc.38 的「请求策略」只覆盖渠道亲和、重试与渠道健康，没有按用户的 IP 数限制。用户缓存 schema 上游仍为 v2，内部 v3 不变。

TokenAuth 链覆盖补齐（这几处原先都没有 `UserIpLimit()`）：

- rc.38 新增的 Responses WebSocket：握手路由 `GET /v1/responses` 挂在 `relayV1Router` 的 `UserIpLimit()` 之后；每个 `response.create` 事件走 `controller/responses_websocket.go` 的内部 gin 引擎，链上加 `UserIpLimit()`（该引擎 `RemoteAddr` 取握手时解析出的客户端 IP），长连接期间每个事件都刷新滑动窗口。
- rc.39 新增的任务插件 `openai_image.generate/edit` 协议链。
- 早于本次的缺口：`router/task-router.go` 的 `/v1/tasks/:key` 提交与查询、`router/plugin-router.go` 的插件动态任务路由。
- 未覆盖：`TokenOrTaskArtifactAccessAuth` 的 Bearer 回退（任务产物内容下载），该中间件内部直接调用 `TokenAuth()(c)`，需要时另行处理。

管理端「清除活动 IP」错误改用上游 `common.ApiErrorT` 英文源文本；relay 拒绝消息 `user.ip_limit_reached` 属于发给 AI 客户端的后端文案，按上游新规保留在 `i18n/keys.go`（en / zh-CN / zh-TW）。PAT 规则 `DELETE /api/user/:id/ip_limit` 需 `user:write`；用户编辑里的 `concurrent_ip_limit` 与分组、备注同级，不触发上游 rc.41 的管理员二次校验。

## 2026-09-09 rc.36 核对

继续保留，上游没有等价的滑动窗口 IP 限制。所有 relay、video 和五条插件协议 TokenAuth 链均保留 `UserIpLimit()`。上游缓存 schema 仍为 v2，内部 v3 不需要递增。

用户更新保留上游 `StructExcept(..., "Password")` 校验，不复活旧密码占位符；另保留 `ConcurrentIpLimit >= -1` 校验及入库字段。三数据库新建/升级/重复迁移验证通过，详情见 [同步记录](../sync-rc36-2026-09-09.md)。

## 为什么要这个改动

防止单个用户的 key 被分发到大量客户端共享使用：在 token 鉴权的 relay 路由上，限制每个用户在滑动时间窗内（默认 10 分钟）最多同时使用 N 个不同客户端 IP。白名单 IP/CIDR 不计数，部署在服务器上的 key 不受影响。

## 改动内容

- `middleware/user_ip_limit.go` — 滑动窗口去重 IP 集合：Redis ZSET + 原子 Lua 脚本实现；Redis 未启用时用同语义内存实现兜底
- `setting/user_ip_limit.go`、`model/option.go` — 站点级选项：`UserIPCountLimit`（0 = 关闭）、`UserIPWindowMinutes`、`UserIPWhitelist`（保存时校验）
- `model/user.go`、`model/user_cache.go`、`model/user_auth_cache.go` — 每用户覆盖列 `users.concurrent_ip_limit`：0 跟随全局，正数覆盖，-1 豁免；经 UserBase 缓存（schema v3）
- `controller/user.go`、`router/api-router.go` — 管理端 `DELETE /api/user/:id/ip_limit`，清除某用户已记录的活动 IP
- `router/relay-router.go`、`router/video-router.go` — 中间件挂到 relay 路由
- `router/task-plugin-protocol-router.go`、`router/video-router.go` 的 `videoSharedRouter` — 2026-09-01 同步 rc.30 后补挂：上游 #7076 把内置任务 adaptor（/suno、/kling/v1、/jimeng、/v1/video/generations 提交路由）换成了沙箱 JS 插件系统，旧路由块已删除，`UserIpLimit()` 改挂到插件协议路由的五条 TokenAuth 链上
- `web/src/features/system-settings/request-limits/rate-limit-section.tsx`、`web/src/features/users/**` — 安全限流设置区块与用户列表行操作/编辑抽屉里的每用户覆盖
- 测试：`middleware/user_ip_limit_test.go`、`setting/user_ip_limit_test.go`

## 部署影响

- **数据库迁移**：`users` 表新增 `concurrent_ip_limit` 列（GORM `default:0`，跟随全局）。
- 新增三个站点选项（见上），默认 `UserIPCountLimit=0` 即关闭，现网行为不变。
- 用户缓存 schema 升到 v3，部署后旧缓存条目自然失效重建，无需人工操作。

## 与上游的冲突风险

`router/relay-router.go` 的中间件链和 `model/option.go`、`web` 设置页都是上游常改文件，属于机械冲突取并集。`model/user.go` 的列与 `user_auth_cache.go` 的缓存 schema 需确认上游没有同名列或自己的 schema version 递增（冲突时内部的 v3 要顺延）。

2026-09-01 同步 rc.30 实况：上游 #6865 重构了 relay 路由文件、#7076 删除了内置任务路由（suno/kling/jimeng/video 提交口），任务提交改由 `pkg/jsplugin` 插件协议动态注册。今后同步时重点确认 `router/task-plugin-protocol-router.go` 的各条 TokenAuth 链仍带 `UserIpLimit()`，以及上游是否新增了不带它的任务类路由。

## 验证方式

```bash
go test ./middleware -run UserIPLimit
go test ./setting -run UserIPLimit
cd web && bun run typecheck
```

手工验证：开启全局限制为 1 后，用同一把 key 从两个不同 IP 各发一次 relay 请求，第二个 IP 被拒；把该用户 `concurrent_ip_limit` 设为 -1 后两个 IP 都放行；管理员调 `DELETE /api/user/:id/ip_limit` 后第一个 IP 立即可用。

## 退出条件

上游收编等价功能（每用户并发 IP 限制 + 白名单 + 每用户覆盖）后移除内部实现，条目移入已归档。
