# Langfuse Conversation Tracing — Master Plan (分批执行索引)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement each sub-plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 按 `docs/superpowers/designs/2026-08-12-langfuse-conversation-tracing-design.md`（下称"设计文档"）为 New API 增加可选的 Langfuse OTLP trace 导出。

**Architecture:** 数据面包 `service/langfuse`（Recorder/capture writer/sampler，只依赖 `common`/`constant`/`relay/common`/`relay/constant`/`relaykit/dto`/`relaykit/types`/`setting/langfuse_setting`）+ 控制面包 `service/langfuseconfig`（持久化/reconcile/publish，可依赖 `model`）+ OTel SDK（BatchSpanProcessor + otlptracehttp）。controller 在 RelayInfo 创建后 `Begin`，共享 `doRequest` 的 `relayClient.Do` 前 `BeginAttempt`，handler 返回后 `EndAttempt`，统一 finalizer `Finish`；结算函数把最终 usage/quota 记到 active attempt。

**Tech Stack:** Go 1.22+, Gin, GORM, OpenTelemetry Go SDK v1.44（plan-3 执行时从 v1.34 上调；otel/sdk、otlptracehttp、proto/otlp 仅测试）、React 19 + TanStack + react-hook-form/zod、i18next（7 语言）。

## 子计划与执行顺序

严格按序执行；每个子计划独立可编译、可测试、可提交。编号即 `docs/superpowers/plans/2026-08-14-langfuse-conversation-tracing/` 下的文件。

| 序 | 文件 | 内容（设计文档 §16 对应） | 前置 |
|---|---|---|---|
| 0 | `plan-0-billing-prereq.md` | tiered billing / channel cache semantic 前置改动：`resolvePromptCacheInclusion`、`summary.UpstreamPromptTokensIncludeCache`/`InputExcludesCache`、4 参 `BuildTieredTokenParams`（§16.0、§8.4） | 无 |
| 1 | `plan-1-otel-deps.md` | OTel 依赖转 direct 并锁定版本（§16.1） | 0 |
| 2 | `plan-2-config-controlplane.md` | `setting/langfuse_setting` 配置/校验/快照、专用 GET/PUT、通用 option 拒绝、`service/langfuseconfig` 持久化与 reconcile（§16.2） | 1 |
| 3 | `plan-3-runtime-manager.md` | runtime manager / RuntimeBinding / TryAcquire / exporter（WithEndpointURL、Basic auth、403 RoundTripper）、exporter decorator 计数、shutdown、OTLP 集成契约测试（§16.3、§16.7） | 2 |
| 4 | `plan-4-content-capture.md` | identity.go（session 提取）、content.go（脱敏/合法截断）、writer.go（capture writer）、aggregate.go（四协议聚合）（§16.4） | 3 |
| 5a | `plan-5a-gemini-classifier.md` | **独立 PR**：纯函数 Gemini action/model 分类器 `relayconstant.ClassifyGeminiAction`（§16.5 前半、§2.1） | 无（可与 1–4 并行） |
| 5b | `plan-5b-recorder-controller.md` | Recorder 生命周期（Begin 三阶段/BeginAttempt/EndAttempt/Finish/worker materialization）、controller 统一 finalizer 与 retry loop 接入、`ContextKeyLangfuseRecorder`（§16.5 后半） | 3、4、5a |
| 6 | `plan-6-usage-cost.md` | `PostTextConsumeQuota`/`PostAudioConsumeQuota` 接入最终 usage/cost、UsageRecord 归一化互斥桶（§16.6、§8.4–8.5） | 5b |
| 7 | `plan-7-frontend.md` | 设置 UI、七语言 i18n、前端测试（§16.8、§12） | 2 |
| 8 | `plan-8-verification-e2e.md` | 全量验证 + 真实 Langfuse E2E（§16.9、§14.6） | 全部 |

**分支约定（见 `docs/internal/README.md`）：** 子计划 0 和 5a 是设计文档要求的**独立 PR**，各自从 `origin/main` 切分支（`feat/xxx`），合入后其余子计划在 `feature/langfuse-tracing` 上继续。每个子计划完成后按台账流程更新 `docs/internal/`。

## Global Constraints（所有子计划隐含遵守）

- **JSON**：业务代码 marshal/unmarshal 一律走 `common.Marshal`/`common.Unmarshal` 等 wrapper，禁止直接调用 `encoding/json`（类型引用如 `json.RawMessage` 允许）。
- **数据库**：兼容 SQLite/MySQL≥5.7.8/PostgreSQL≥9.6；GORM 优先；`FOR UPDATE` 用 `lockForUpdate(tx)`。
- **relaykit 独立性**：不改动 `relaykit/`（子计划 0 例外：允许像 `anthropic_messages_exclude_cache` patch 那样在 `relaykit/dto/channel_settings.go` 加字段，改完必须 `cd relaykit && GOWORK=off go build ./...` 验证）。
- **billing 安全**：quota 换算只用 `common/quota_math.go` 的 helper；`*Checked` 变体 + `attachQuotaSaturation`。
- **测试**：新 Go 测试用 `testify/require`（setup/fatal）+ `assert`（值比较）；禁止无意义覆盖测试、随机 fuzz、sleep 判序。
- **包依赖白名单**（设计文档 §4.1）：`service/langfuse` 直接 import 只允许 `common`、`constant`、`relay/common`、`relay/constant`、`relaykit/dto`、`relaykit/types`、`setting/langfuse_setting`；闭包禁止根 `service`、`model`、`controller`、`service/langfuseconfig`、`relay/channel/**`；任何 `relay/**` 包不得依赖 `service/langfuseconfig`。由 AST/import 测试固定。
- **受保护标识**：不得移除/改名 new-api 与 QuantumNous 的任何品牌、版权、模块路径标识。
- **i18n**：前端文案全部 `t('English key')`；locale 写入只经 `web/scripts/add-missing-keys.mjs`（临时脚本，用完删除），禁止直接编辑 locale JSON；流程见 `.agents/skills/i18n-translate/SKILL.md`。
- **每个子计划收尾**：`go build ./...`、受影响包 `go test`、`cd relaykit && GOWORK=off go build ./...`（即使未改 relaykit）。
- **设计文档为准**：子计划中的"按 §X"均指向 `docs/superpowers/designs/2026-08-12-langfuse-conversation-tracing-design.md` 的对应章节；实现与设计冲突时停下向用户确认，不得隐式偏离（尤其设计文档反复强调的"已知行为，不得顺手修复"清单：Gemini 分发不一致、AWS SDK/Xunfei 旁路、非 Gemini 格式被 Gemini 渠道映射为 embedding 的行为）。
