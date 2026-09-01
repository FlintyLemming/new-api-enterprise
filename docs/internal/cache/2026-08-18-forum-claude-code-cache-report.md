# Claude Code × 本地 DeepSeek：两则缓存问题，以及顺带修好的压缩时机

社内技术报告 · 2026-08-18

面向在 new-api（8850）上用 Claude Code、走本地 H200 DeepSeek-V4-Flash 的同事。结论先说：

1. **Claude Code 前缀几乎不命中**——已修好，命中率从约 14% 回到 80%+。
2. **GPU 被挤出后本该从内存（L2）回读，实际从未回读**——原因查清，不是配置漏开。等上游修，或周末我们自己改 vLLM lookup。
3. **Claude Code 用量显示偏高、上下文压缩偏早**——回包口径已按 Anthropic 语义改对。数字正常后，客户端不会再因为「多算了」而提前触发自动压缩。

下面分三块写清楚：发生了什么、怎么验证的、现在处于哪一步。

---

## 0. 我们这条链路在干什么

Claude Code 走 Anthropic 的 `/v1/messages`。网关把请求转成 OpenAI chat，打到本机 8 卡 H200 上的 DeepSeek-V4-Flash（vLLM 0.26.0，开了 DSpark 投机解码）。

推理侧有两层前缀缓存：

| 层 | 在哪 | 作用 |
| -- | -- | -- |
| L1 | GPU KV | 同一前缀下一轮不用重算。命中会出现在 usage 的 `cached_tokens` 里 |
| L2 | 主机内存约 960 GB | GPU 放不下、被挤出的前缀，按理应从内存 DMA 回显存 |

Claude Code 自己还有一套「上下文快满了就 compact（压缩摘要后续聊）」。它看的是接口返回的 `input_tokens`，不是我们后台日志里的 `prompt_tokens`。

这三件事原来是拧在一起的：前缀不命中 → 预填充又慢又贵；回包把缓存也算进 `input_tokens` → 客户端以为窗口更满 → 更早 compact → 又开一条几乎无缓存的新会话。

---

## 1. 已解决：Claude Code 每个请求都改了前缀第 1 段

### 现象

同一块 GPU、同一个模型，两条客户端差五倍：

| 入口 | 8/17 命中率 | 说明 |
| -- | --: | -- |
| Reasonix 等，直打 `/v1/chat/completions` | **83–91%** | system 稳定，`You are Reasonix...` |
| Claude Code，`/v1/messages` → `llm-prime` | **14%** | 连续两天同一数量级（8/14 已是 16%） |

8/17 那天 Claude Code 451 次请求里，有 **383 次缓存长度恰好是 17920**（280×64，DeepSeek 按 64 token 对齐），和对话从 4 万涨到 18 万无关。也就是：只有工具定义那一截能命中，历史全部当新前缀重算。

### 原因

Claude Code（VS Code 入口）每个请求的 `system[0]` 都是：

```text
x-anthropic-billing-header: cc_version=...; cc_entrypoint=claude-vscode; cch=<每次不同>;
```

这是给 **Anthropic 官方账单** 用的探测头，本地 DeepSeek 不认。网关把 Claude 的多段 system 按顺序拼成一条 OpenAI system 时，这段头变成发给 GPU 的第一个 token。

DeepSeek / vLLM 的前缀缓存是链式的：第 1 块变了，后面全部失效。`cch=` 每次都换，所以同一会话的下一轮也无法复用上一轮已经算过的 system 和历史。

工具定义排在这段头前面，所以还能剩约 17920 token 的固定命中——这就是那个精确的 14%。

Langfuse 里后两段 system（「You are Claude Code」和主提示）全天哈希不变，git status 也没每轮改。不是「用户内容太碎」，就是这个头。

### 处理

new-api 加了渠道开关 `strip_anthropic_billing_header`（默认关）。打开后，只在 Claude → 非 Claude 上游、把 system 压成一条 string 时，丢掉以 `x-anthropic-billing-header:` 开头的那一块。

- 不改客户端请求，不改计费公式
- Langfuse 仍能看到原始头（抓的是转换前）
- 官方 Anthropic / OpenRouter Claude 那条路不过滤

生产上给 H200 这条渠道打开后，同一类 Claude Code 流量（仍每轮带变化的 `cch=`、仍在 compact 续聊）：

| | `/v1/messages` 命中率 | 缓存钉死在 17920 |
| -- | --: | --: |
| 修之前（8/17） | 14.3% | 383 / 451 |
| 修之后（8/18） | **83.1%** | **0** |

连续几轮变成「第一轮冷、后面 98–99%」，和 Reasonix 同一形态。对照用户只开 Claude Code 一路、同样带着这个头，命中率约 91%。

剩下到不了 95%+ 的那一截，主要是多客户端并行、超长上下文把 GPU 挤爆后的整段冷启动，不是前缀每轮还在变。详见下面第 2 节。

---

## 2. 未解决：L2 只写不读（原因已清楚）

### 现象

compose 里开了 `OffloadingConnector`，约 960 GB pinned 主机内存，本意是 GPU 挤不下时从内存回读。

vLLM 从 **8/07 02:48** 启动到写这篇时，只读扫过完整日志：

| 模式 | 行数 |
| -- | --: |
| `kv_offload_store_bytes`（写入） | **198422** |
| `kv_offload_load_bytes`（回读） | **0** |
| `External prefix cache hit rate: 0.0%` | **413174** |
| External 非 0 | **0** |

写了近 20 万次指标点，回读一次都没有。启动后第一分钟就已经是这个形状：每秒往 CPU 送 10–25 GiB，`External prefix cache hit rate` 恒为 0%。11 天后 L1 已到 88%，External 仍是 0%。

`cpu_cache_usage_perc` 经常掉回 0，**不能当成内存池是空的**。这个数只统计正在 pin 的 DMA；写完的块变成 evictable，故意不计。判断有没有读，只看 `load_bytes` 和 External hit。

### 原因（不是配置）

`OffloadingConnector` 的 lookup 要求 **每一个 KV group 都要命中**。任一 group 返回 0，整次 load 放弃——已经写到 CPU 的 MLA 前缀也不 promote。

DeepSeek V4 + DSpark 正好有多组对不齐的 KV：

- 全量 MLA：block 256，store 正常（那几十 GB/s 就是它）
- compressor 滑动窗口：block 只有 4 或 8
- DSpark/MTP 被标成 eagle 组：启动日志写明 **decode 故意不存最后一块**

续聊前缀里带着上一轮生成的 token，eagle 尾巴按设计没落盘 → lookup 从尾巴对不上 → 整段 return 0 → GPU 当冷启动重算。

配置是齐的：`kv_both`、`cpu_bytes_to_use=960e9`、`CuMemAllocator` 都在工作。换成 `--kv-offloading-size` 也是同一套 connector，救不了。

### 态度

**先不在生产上关 L2、也不热改 vLLM。** 查清就可以，改 lookup 要动调度器，不适合在业务时段碰这套 8 卡服务。

两条后续，二选一或并行：

- **等上游**：vLLM OffloadingConnector 在 DSV4+DSpark 混合 KV 上，SWA/eagle miss 时不要否决已命中的 MLA。
- **周末自己改**：同一处 lookup，miss 时仍 promote 全量 MLA，SWA 现算。修好的判据是日志里出现 `kv_offload_load_bytes > 0`，且 GPU 很低时再来一条刚被挤掉的长前缀，External 不再是 0%。

在那之前，这 960 GB pinned 内存对命中率没有贡献，只占主机内存和写带宽。周末若决定先卸掉，关 OffloadingConnector 不会让 L1 变差。

---

## 3. 顺带：Claude Code 的数字对了，压缩也不会提前

这和上面两则不是同一层，但用户体感是绑在一起的。

OpenAI 兼容上游（包括我们的 DeepSeek）习惯把缓存命中算进 `prompt_tokens`。网关转成 Anthropic Messages 时，若原样塞进 `input_tokens`，Claude Code 会以为「未命中的输入」有整段上下文那么长。

更糟的是流式：`message_start` 先带一版**预估** prompt，客户端拿它和终态 usage 取 max。预估不含「这是缓存」的信息，偏差再放大一截。

Claude Code 的自动压缩看的就是这套 `input_tokens`。数被抬高 → 它以为窗口更满 → **比真实上下文更早 compact**。compact 之后整段历史换成摘要，前缀全换，L1 再好也接不上。

8/13 合入的渠道开关 `anthropic_messages_exclude_cache`（H200 渠道已开）只改回给客户端的 usage：

- 终态：`input_tokens = prompt_tokens − cache_read − cache_creation`，缓存单独放在 `cache_read_input_tokens`
- 流式 `message_start` 不再发预估，避免取 max 叠高

**计费和后台日志不变**，仍用上游原始 usage。变的是 Claude Code 看到的世界：统计对了，压缩阈值也按「真正的新输入」来，而不是按「含缓存的全长」。

第 1 节修好之后，真实 `cache_read` 会大幅上升，这个口径才有意义：以前几乎没有缓存可读，扣不扣都差不多；现在 80%+ 命中，若不扣，客户端会把已经命中的十几万 token 当成新输入，压缩会明显提前。

---

## 4. 三件事叠在一起时，用户会感觉到什么

| 以前 | 现在 / 下一步 |
| -- | -- |
| Claude Code 每轮改 `cch=`，GPU 前缀作废，只有约 1.8 万工具 token 能命中 | 头已剥掉，会话内 98–99%，整天约 83% |
| 长会话一挤出 GPU，只能整段重算（L2 从未回读） | 原因已定位；等上游或周末改 lookup。在此之前并行开三条 20 万+ 会话，冷启动仍然会有 |
| 回包 `input_tokens` 含缓存 + 流式预估取 max → 用量虚高、压缩偏早 | 回包按 Anthropic 语义；压缩应按真实新输入触发 |

对日常用 Claude Code 的同事：同一会话里续聊应明显更快（少做预填充），也不该再动不动就「上下文满了，我先总结一下」。若仍频繁压缩，更可能是真的聊得很长，或同时开了多个 Agent 把 GPU 挤满，而不是网关又把数报错了。

---

## 5. 我们不改什么

- 套餐扣费公式现在仍是 `p × 3 + c × 3`，表达式里没有缓存折扣。命中率上去，**主要省的是 H200 预填充**，不是自动少扣额度。
- 不在业务时段重启或热补 vLLM。
- 不把「去头」做成用户可填的通用前缀编辑器。目前只有这一个已知的、每轮都变的头。

---

## 6. 相关记录

更细的调查笔记（含 SQL、完整日志片段、代码锚点）在 new-api 仓库：

- [缓存优化目录](README.md)
- [计费头打断前缀缓存](2026-08-18-claude-code-billing-header.md)
- [L2 只写不读](2026-08-18-vllm-l2-offload-write-only.md)
- 回包口径 patch：[feat-anthropic-messages-cache-usage](../patches/feat-anthropic-messages-cache-usage.md)
