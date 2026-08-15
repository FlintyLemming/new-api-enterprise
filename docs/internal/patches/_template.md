# <分支名>

| 项 | 值 |
| -- | -- |
| 分支 | `<type>/<topic>` |
| 基线 | `origin/main` @ `<切分支时的 commit>` |
| 合入 commit | `<merge commit>` |
| 合入日期 | `YYYY-MM-DD` |
| 状态 | `internal-only` / `upstream-pending` / `upstream-merged` / `dropped` |
| 上游 PR | `<链接或「未提交」>` |

## 为什么要这个改动

内部遇到的具体问题，以及不改会怎样。写清楚触发场景，日后判断「上游变了之后这条还需不需要」全靠这段。

## 改动内容

按模块列，每条说清楚行为变化而不是复述 diff。

- `path/to/file.go` — 做了什么
- `web/src/...` — 做了什么

## 部署影响

新增/改名的渠道设置、系统设置、环境变量、数据库迁移，以及升级到这个版本之后需要人工做的操作。没有就写「无」。

## 与上游的冲突风险

这条 patch 碰到的、上游也在频繁改动的文件，以及合并时的处理原则（保留内部实现 / 取上游 / 需要重新适配）。

## 验证方式

能重复执行的命令，以及必要的手工验证步骤。

```bash
# e.g. cd relaykit && GOWORK=off go test ./...
```

## 退出条件

什么情况下可以把这条从 `internal-custom` 移除。
