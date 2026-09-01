# feature/user-concurrent-ip-limit

| 项 | 值 |
| -- | -- |
| 分支 | `feature/user-concurrent-ip-limit` |
| 基线 | `origin/main` @ `ccd535ef8` |
| 合入 commit | 线性合入（无 merge commit），tip `7ddc7866` |
| 合入日期 | 2026-08-24 |
| 状态 | `upstream-pending` |
| 上游 PR | 未提交 |

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
