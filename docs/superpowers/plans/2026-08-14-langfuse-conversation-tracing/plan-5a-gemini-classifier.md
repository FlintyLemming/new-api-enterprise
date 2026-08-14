# Plan 5a — 独立 PR：纯函数 Gemini action/model 分类器

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 设计文档 §2.1 与 §16.5 前半——新增 `relayconstant.ClassifyGeminiAction(path, modelName)` 纯函数分类器，**作为独立 PR**（从 `origin/main` 切 `feat/gemini-action-classifier`）。Langfuse（plan-5b）只读消费其结果。

**Architecture:** 单文件纯函数放 `relay/constant`（无任何新依赖、无副作用）。**不得**改动 `controller.geminiRelayHandler`、`GetAndValidateRequest`、Gemini adaptor `GetRequestURL` 或 `DoResponse` 的任何现有分支（设计文档明确：现有四处分发不一致是已知生产问题，不属于本 PR 修复范围）。

**Tech Stack:** Go + testify 表驱动。

## Global Constraints

- 见 `00-master.md`。
- 判定顺序固定："最终 model 前缀优先于 path action"。
- 纯函数：不读全局配置、不 panic、对空入参返回 Unknown。

## 分类规则（唯一权威）

输入：入站 HTTP path（可能含 query 前的完整 path）与**已映射后的最终上游 model 名**。输出四值之一：

1. `strings.HasPrefix(model, "imagen")` → `Predict`；
2. model 前缀为 `text-embedding` / `embedding` / `gemini-embedding` 之一 → `Embedding`（不区分 embedContent/batchEmbedContents，上游 action 区分不属于本分类器职责）；
3. model 不属于上述前缀时看 path 中显式 `:action`（取 path 最后一个 `/` 段中最后一个 `:` 之后、`?` 之前的部分）：
   - `generateContent` / `streamGenerateContent` → `Generate`；
   - `embedContent` / `batchEmbedContents` → `Embedding`；
   - `predict` → `Predict`；
   - 其他任意 action（含空）→ `Unknown`。`/v1/engines/:model/embeddings` 这类**无 `:action`** 的路径在非 embedding/Imagen model 下归 `Unknown`（`/embeddings` 后缀不是可识别 action，不得特判为 embedding）。

---

### Task 1: 分类器 + 契约测试（TDD）

**Files:**
- Create: `relay/constant/gemini_action.go`
- Test: `relay/constant/gemini_action_test.go`

**Interfaces:**
- Produces（plan-5b 消费，签名不得偏离）:

```go
package relayconstant

type GeminiAction int

const (
	GeminiActionUnknown GeminiAction = iota
	GeminiActionGenerate
	GeminiActionEmbedding
	GeminiActionPredict
)

// ClassifyGeminiAction 按设计文档规则判定一个 Gemini relay 请求的最终动作分类。
// path 是入站请求 path(不含 query);modelName 是模型映射完成后的最终上游模型名。
func ClassifyGeminiAction(path string, modelName string) GeminiAction
```

- [ ] **Step 1: 写失败测试**（表驱动，逐行来自 §14.3）：

| path | modelName | 期望 |
|---|---|---|
| `/v1beta/models/gemini-2.5-flash:generateContent` | `gemini-2.5-flash` | Generate |
| `/v1beta/models/gemini-2.5-flash:streamGenerateContent` | `gemini-2.5-flash` | Generate |
| `/v1beta/models/text-embedding-004:embedContent` | `text-embedding-004` | Embedding（前缀优先） |
| `/v1beta/models/x:batchEmbedContents` | `embedding-custom` | Embedding（前缀优先于 path） |
| `/v1beta/models/imagen-3:predict` | `imagen-3` | Predict |
| `/v1beta/models/gemini-2.5-flash:generateContent` | `text-embedding-004` | **Embedding**（渠道映射后前缀优先） |
| `/v1beta/models/gemini-2.5-flash:generateContent` | `gemini-embedding-001` | Embedding |
| `/v1beta/models/gemini-2.5-flash:generateContent` | `imagen-3` | **Predict**（前缀优先于 path action） |
| `/v1/engines/gemini-2.5-flash/embeddings` | `gemini-2.5-flash` | **Unknown**（无 `:action`） |
| `/v1/engines/text-embedding-004/embeddings` | `text-embedding-004` | Embedding（model 前缀先命中） |
| `/v1beta/models/gemini-2.5-flash:countTokens` | `gemini-2.5-flash` | Unknown（未知 action） |
| `/v1beta/models/gemini-2.5-flash:generateContent?alt=sse` | `gemini-2.5-flash` | Generate（query 由调用方剥；测试同时断言含 `?` 时不误判——实现里按"最后一个 `:` 之后、`?` 之前"截） |
| `""` / `"/"` | `` / `gemini-2.5-flash` | Unknown |

- [ ] **Step 2: 确认失败** Run: `go test ./relay/constant -run TestClassifyGeminiAction -v`
- [ ] **Step 3: 实现**（约 30 行；action 提取注意 `strings.LastIndex(segment, ":")` 与 `?` 截断；无 `:` → 无 action → Unknown 路径）
- [ ] **Step 4: 通过 + 不变性检查**

Run: `go test ./relay/constant -v && go build ./... && git diff --stat origin/main -- controller/ relay/channel/gemini/ relay/helper/valid_request.go`
Expected: 测试通过；diff 中**不出现** controller/gemini adaptor/validator 的改动。

- [ ] **Step 5: Commit 与台账**

```bash
git add relay/constant/gemini_action.go relay/constant/gemini_action_test.go
git commit -m "feat(gemini): pure-function action/model classifier for relay consumers"
```

按台账流程补 `patches/feat-gemini-action-classifier.md` 与索引行（`docs(internal)` 单独提交）。
