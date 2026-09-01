# Claude Code 计费头打断 DeepSeek 前缀缓存

| 项 | 值 |
| -- | -- |
| 日期 | 发现 2026-08-17，回看 2026-08-18 |
| 环境 | 8850 new-api 生产，渠道 5 `[H200] Deepseek V4 Flash` |
| 样本用户 | `user_id=417`（`konghua` / 孔华） |
| 观测 | `newapi.logs`（ClickHouse，`insight_cache_tokens`）+ Langfuse `events_full`（8858，原始请求） |
| 修复 | 渠道开关 `strip_anthropic_billing_header`，channel 5 已打开 |
| 当时镜像 | `new-api:langfuse-a5dfe631`（2026-08-17 部署） |
| 相关代码 | `feat/strip-claude-system-prefix`；设计见 [2026-08-17-strip-anthropic-billing-header-design.md](../superpowers/designs/2026-08-17-strip-anthropic-billing-header-design.md)；patch 台账见 [feat-strip-claude-system-prefix.md](../patches/feat-strip-claude-system-prefix.md) |
| 结论 | **已解决。** 客户端仍每轮发送变化的 `cch=`，网关剥掉后上游前缀稳定，缓存随对话增长。 |

本文不含请求正文、模型输出、令牌明文或数据库口令。Langfuse 侧只记结构（system 块哈希、是否含某前缀、会话是否 compact）。

---

## 1. 发现

2026-08-17 孔华当天整体缓存命中率约 **48%**，明显低于他 8/10–8/13 的 73–91%。拆开之后不是「模型或通道坏了」，而是两条客户端差了大约五倍：

| 8/17 入口 | 请求 | prompt | 缓存 | 命中率 |
| -- | --: | --: | --: | --: |
| `/v1/chat/completions` 直打 `deepseek-v4-flash-0731`（Reasonix） | 444 | 5669 万 | 4744 万 | **83.67%** |
| `/v1/messages` → `llm-prime` → 同一上游（Claude Code） | 451 | 4811 万 | 687 万 | **14.29%** |
| `/v1/chat/completions` → `llm-prime` | 7 | 132 万 | 19 万 | 14.31% |

两边都是 channel 5、同一 token、同一 H200 模型。`llm-prime` 只做了模型名映射。

对照更早几天也能对上，不是偶发抖动：

| 日期 | 入口 | 命中率 | 说明 |
| -- | -- | --: | -- |
| 8/10–8/13 | 几乎全是 `/v1/chat/completions` | 73–98% | Reasonix / 同类 OpenAI 客户端 |
| 8/14 | `llm-prime` `/v1/messages` | **15.96%** | 第一次大量走 Claude Code |
| 8/14 | 同日 `llm-prime` `/v1/chat/completions` | 93.11% | 同一模型、另一入口仍正常 |
| 8/17 | 再走 `/v1/messages` | **14.29%** | 与 8/14 同一数量级 |

仪表盘上的「今天命中率低」是「一半高命中 + 一半被钉死在 14%」的加权平均。

---

## 2. 调查方法

1. `newapi.logs` 按 `user_id`、`request_path`、`model_name`、`insight_cache_tokens` 拆。`insight_cache_tokens` 来自 `other.cache_tokens`，计费路径是 `usage_billing_path=upstream`（H200 返回的 usage）。
2. Langfuse `events_full` 看原始 Claude / OpenAI 请求结构（转换前）。`traces` / `observations` 在本套 4.2.0 v4 events_only 部署里是空的，正文在 `events_full`。
3. 对 Claude `system` 三块和 `tools` 做 `sipHash64`，看哪一段在变。
4. 对照 `ClaudeMessagesRequestToOpenAIChat` 怎么把 system 拼成一条 string。

---

## 3. 原因

### 3.1 客户端每轮改第一段 system

Claude Code（VS Code 入口，`cc_entrypoint=claude-vscode`，`cc_version=2.1.116.d8c`）的 `system` 固定三块：

| 块 | 内容 | 8/17 稳定性 |
| -- | -- | -- |
| 0 | `x-anthropic-billing-header: cc_version=...; cc_entrypoint=claude-vscode; cch=<5 位十六进制>;` | **每请求唯一** |
| 1 | `You are Claude Code, Anthropic's official CLI...` | 全天同一哈希 `88D95ABDDD0F5572` |
| 2 | 主系统提示（工作区、分支、git status 快照等） | 全天同一哈希 `363BFB9E19A6BCC3` |
| tools | 23 个工具，约 7.4 万字符 JSON | 全天同一哈希 |

连续几拍的 `cch` 完全不同，例如 `8c7f0` → `7ee55` → `490dc`。这是 Claude Code 给 **Anthropic 官方账单** 用的探测头，new-api 和本地 DeepSeek 都不读。全库搜不到这个字段名。

块 2 里虽有 git status / 工作区，但 Claude Code 写明会话中不更新；当天哈希不变。所以不是「git status 每轮在变」。

### 3.2 转换层把变化的头拼到最前面

`/v1/messages` 会转成 OpenAI Compatible。非 OpenRouter Claude 路径把所有 system 段按顺序拼成一条 string：

```123:129:relaykit/relayconvert/internal/claude_messages/to_oai_chat_req.go
				} else {
					systemStr := ""
					for _, system := range systems {
						if system.Text != nil {
							systemStr += *system.Text
						}
					}
					openAIMessage.SetStringContent(systemStr)
```

发给 H200 的 system 实际以这段开头：

```text
x-anthropic-billing-header: cc_version=2.1.116.d8c; cc_entrypoint=claude-vscode; cch=XXXXX;You are Claude Code...
```

DeepSeek / vLLM 前缀缓存是链式的：第 1 个 block 的 hash 变了，后面所有 block 的 hash 都变。`cch=` 正好在 token 0 附近，同一会话的下一轮也无法复用上一轮已经算过的 system 和历史。

### 3.3 为什么不是 0%，而是精确的约 14%

8/17 `/v1/messages` 的缓存长度几乎是常数：

| 缓存 token | 请求数 | 含义 |
| -- | --: | -- |
| 0 | 28 | 完全冷 / 被挤出 |
| 256 | 40 | 只剩模板级极短前缀 |
| **正好 17920** | **383** | 固定前缀命中，之后全部 miss |

451 次里有 383 次缓存 **恰好 17920**（280 × 64，DeepSeek 按 64 token 对齐），和 prompt 从 4 万涨到 18 万无关。23 个工具 JSON ≈ 7.4 万字符，按约 4 字符/token 估算约 1.85 万，和 17920 对得上——工具定义在模板里排在变化的 system 前面，所以只有这一截能命中。平均 prompt 约 10.7 万，17920 / 106667 ≈ **14.3%**，与当天 14.29% 一致。

Reasonix 的 system 以稳定的 `You are Reasonix, a coding agent.` 开头，327+ 次请求前 500 字节几乎不变，缓存随对话涨到 12 万+，所以是 80% 出头。

### 3.4 次要因素（不是根因）

- 当天 Claude 会话几乎全部是 compact 续聊（「This session is being continued from a previous conversation that ran out of context」）。compact 会整段改写第 1 条 user，跨 compact 周期无法共享对话前缀。这只解释「周期边界会冷一下」，解释不了周期内每一轮都 miss。
- 09:00–14:00 Claude 与 Reasonix 同时打同一块 H200，会争 KV。Reasonix 前缀热、可共享，仍能维持高命中；Claude 前缀每轮唯一，争抢也救不回来。
- 渠道没有额外 `system_prompt`，不是网关又插了一段变化前缀。

---

## 4. 解决方法

new-api 当时没有「丢掉这段头」的专用能力。`system_prompt_override` 只会往前插；`RemoveDisabledFields` 只剥顶层 JSON 键；`param_override` 的 `regex_replace` 能workaround，但不是产品功能。

做成 **渠道开关**，默认关：

- 字段：`ChannelSettings.strip_anthropic_billing_header`
- 只在 `ClaudeMessagesRequestToOpenAIChat` 把 system **压成一条 string** 时过滤
- 认头：text 块 `TrimSpace` 后以 `x-anthropic-billing-header:` 开头则整块丢掉；字符串 system 则去掉第一行
- OpenRouter Claude 分块路径不过滤（那条路还可能把头交给真正的 Claude）
- 不改 `ClaudeRequest.System` 原文，所以预扣估算和 Langfuse 原始抓包仍看得到这段头
- 画面说明里写死会删哪些头（本期只有这一个）

上线后给 **channel 5** 打开。同一渠道上的 Reasonix / opencode 走 `/v1/chat/completions`，请求里没有这段头，不受影响。回滚只需关掉开关，不必回镜像。

不在这期做的：用户自定义前缀列表、改计费表达式、用 `param_override` 顶替。

---

## 5. 回看结果（2026-08-18）

先确认用法没换。Langfuse 当天（截至回看时）：

| 客户端 | 入口 | 是否仍带计费头 | 是否 compact 续聊 |
| -- | -- | -- | -- |
| Claude Code | `claude llm-prime` / `/v1/messages` | 是，每个请求 `cch` 仍变；块 1/2 哈希与 8/17 相同 | 是，全部续聊 |
| Reasonix | `openai deepseek-v4-flash-0731` | 无 | 否 |
| opencode | `openai llm-prime` | 无 | 否 |

对照实验成立：还是 Claude Code + 每轮 `cch=` + compact，只是网关剥了头。

`/v1/messages` 命中率：

| | 请求 | 命中率 | 缓存钉在 17920 |
| -- | --: | --: | --: |
| 8/17 | 451 | **14.29%** | **383** |
| 8/18 | 54 | **83.09%** | **0** |

8/18 `/v1/messages` 缓存桶：5 次 0、4 次 256、3 次不足 64k、14 次 64k–128k、28 次 128k+。高桶里 `avg_cache ≈ avg_prompt`，已经是「对话前缀命中」而不是「只命中工具」。

连续几轮（同一小时内，上海时间）：

```
09:28  prompt 109974 → cache    256   0.2%   冷启动
09:28         111232 →      109824  98.7%
09:28         111422 →      111104  99.7%
09:36         118702 →           0   0%     间隔 / KV 挤出后再冷
09:37         119455 →      118528  99.2%
```

偶发的 0 / 256 是正常冷启动或 H200 KV 被挤出，不是前缀每轮被改掉。

同日另外两条 OpenAI 入口：`llm-prime` `/v1/chat/completions` 83.01%，`deepseek-v4-flash-0731` 74.18%（后半段 Reasonix 自己也有波动，和这次修的头无关）。Claude 这条已经和它们同一水平。

---

## 6. 计费影响（需要单独记一笔）

开关**不改** new-api 计费公式。孔华走订阅阶梯：

```text
usage_billing_path = upstream
billing_mode       = tiered_expr
expr               = len <= 256000 ? p * 3 + c * 3 : p * 10 + c * 10
```

表达式没有 `cr`。`BuildTieredTokenParams` 只有表达式真正引用 `cr` 时才会从 `p` 里减掉缓存 token。因此：

- 命中率上去，主要省的是 **H200 预填充算力**
- VIP 额度仍按完整 `prompt_tokens`（含已命中缓存）× 3 扣
- 剥头本身只让每轮 prompt 少约 20–30 token（头文本不再送给模型），可忽略

若以后要「命中缓存就少扣用户套餐」，要改表达式，和这个开关无关。

---

## 7. 以后怎么复查

样本用户 `user_id = 417`。命中率按 token 计：`sum(insight_cache_tokens) / sum(prompt_tokens)`。

```sql
SELECT
  toDate(toDateTime(created_at, 'Asia/Shanghai')) AS d,
  JSONExtractString(other, 'request_path') AS path,
  count() AS reqs,
  sum(prompt_tokens) AS prompt,
  sum(insight_cache_tokens) AS cache,
  if(prompt = 0, 0, round(cache / prompt * 100, 2)) AS hit_pct,
  countIf(insight_cache_tokens = 17920) AS pinned_17920
FROM newapi.logs
WHERE user_id = 417 AND type = 2
  AND created_at >= toUnixTimestamp(toDateTime('2026-08-17 00:00:00', 'Asia/Shanghai'))
GROUP BY d, path
ORDER BY d, prompt DESC
```

判断开关是否仍在起作用：

- `/v1/messages` 的 `pinned_17920` 应接近 0
- 命中率应与同日 `/v1/chat/completions` 同一量级（约 80%+），而不是再掉回 14–16%
- Langfuse 里原始 Claude `system[0]` **仍会**出现 `x-anthropic-billing-header`（抓的是转换前）。这不表示开关失效。

渠道设置确认：

```sql
SELECT id, name, setting
FROM channels
WHERE id = 5;
```

`setting` 里应有 `"strip_anthropic_billing_header": true`。

---

## 8. 还需要知道的

- **只修转换后的上游 body。** 官方 Anthropic / OpenRouter Claude 分块路径故意不剥，以免动到可能被上游认的头。
- **`pass_through_body` 绕过转换**，这个开关对它无效。channel 5 当前未开透传。
- 若以后 Claude Code 换一个同样「每轮都变、又排在 system 最前」的头，要在 `shouldStripClaudeSystemText` 的常量列表和渠道说明里再加一条。不要为此先做通用前缀编辑器。
- 8/14 已经出现过同一症状，当时没有抓请求正文。这次能定位，是因为 8858 Langfuse 从 8/15 起在记完整 chat。
- 内部 patch 状态：`internal-only`。上游 new-api 没有对应问题，不打算提 PR，除非他们也开始把 Claude Code 转到非 Claude 前缀缓存上游。
