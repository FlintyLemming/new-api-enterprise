# 本地 DeepSeek 前缀缓存两起故障的定位与处置

| 项 | 值 |
| -- | -- |
| 报告日期 | 2026-08-18 |
| 涉及系统 | 8850 new-api 网关；8 卡 H200 上的 DeepSeek-V4-Flash-0731（vLLM 0.26.0，DSpark 投机解码） |
| 涉及渠道 | channel 5 `[H200] Deepseek V4 Flash` |
| 数据来源 | ClickHouse `newapi.logs`、Langfuse `events_full`（8858）、vLLM 容器日志（只读） |
| 适用读者 | 在 8850 上使用 Claude Code、opencode、Reasonix 等客户端的同事；关注推理成本的同事 |

本文不含请求正文、模型输出、令牌明文或数据库口令。Langfuse 侧只取结构信息（system 块哈希、是否含某前缀、会话是否 compact）。

---

## 摘要

例行巡检发现，8850 上 Claude Code 一路的前缀缓存命中率长期停在 14% 左右，而同一渠道、同一模型的其他客户端在 83%–91%。定位结论是：Claude Code 每个请求都会在 `system[0]` 放一段每次都变的计费探测头，网关做协议转换时把它拼到了上游 prompt 的最前端，而 vLLM 的前缀缓存是链式哈希，首块一变、后续全废。网关侧增加渠道级过滤后，该入口命中率恢复到 83%，会话内连续轮次可达 98%–99%。

同一轮排查中核实了二级缓存的实际行为，发现主机内存侧的 KV offload 自 08-07 启动以来只写不读：写入指标点累计 198422 次，回读 0 次，External 命中率恒为 0.0%。原因不是 compose 漏配，而是 vLLM `OffloadingConnector` 的 lookup 对混合 KV 采取全组 AND 判定，DeepSeek-V4-Flash 的滑动窗口组与投机解码组按设计不落盘尾块，导致查询整体返回 0，已经写到内存里的 MLA 前缀也不会被提回显存。该问题只能改 vLLM 或等上游，本次未在生产上处理。

此外补记一项 8/13 已合入的相关修正：网关回给 Claude Code 的 `input_tokens` 此前包含缓存命中部分，导致客户端高估上下文占用、过早触发自动压缩。修正后按 Anthropic 语义扣除缓存。这一项在第 1 节修好之前意义有限，修好之后才真正生效。

| 编号 | 问题 | 状态 |
| -- | -- | -- |
| 一 | 计费探测头打断上游前缀缓存 | 已解决，命中率 14.29% → 83.09% |
| 二 | 二级 KV 缓存（主机内存）只写不读 | 根因已定位，未修复，需改 vLLM |
| 三 | 回包 usage 口径导致上下文过早压缩 | 已解决（8/13 合入），依赖问题一才见效 |

---

## 一、系统构成与关键指标

Claude Code 通过 Anthropic 的 `/v1/messages` 接口接入 8850。网关将请求转换为 OpenAI Chat 格式，转发到本机 8 卡 H200 上的 DeepSeek-V4-Flash-0731。Reasonix、opencode 等客户端则直接走 `/v1/chat/completions`，落到同一渠道、同一模型。

推理侧设计了两级前缀缓存：

| 层 | 位置 | 作用 | 观测方式 |
| -- | -- | -- | -- |
| L1 | GPU KV | 同一前缀的后续轮次免于重复预填充 | usage 的 `cached_tokens`，日志字段 `insight_cache_tokens` |
| L2 | pinned 主机内存约 960 GB | GPU 池放不下而被挤出的前缀，命中时按需 DMA 回显存 | vLLM 指标 `kv_offload_load_bytes`、`External prefix cache hit rate` |

命中率按 token 计算，即 `sum(insight_cache_tokens) / sum(prompt_tokens)`。这个指标直接决定 H200 的预填充开销：命中的部分不需要重算，未命中的部分要按完整长度重新做一次 attention 预填充。在十万量级的上下文里，两者的耗时差一个数量级。

需要先说明的是，Claude Code 的自动上下文压缩（compact）读取的是接口返回的 `input_tokens`，而不是网关后台记录的 `prompt_tokens`。这一点使得三个问题在用户观感上是耦合的：前缀不命中导致预填充变慢变贵；回包把缓存计入 `input_tokens` 导致客户端以为窗口更满；提前压缩后整段历史被摘要替换，前缀全部改变，又开出一条几乎没有缓存的新会话。

---

## 二、问题一：计费探测头打断上游前缀缓存

### 2.1 现象

2026-08-17，样本用户（`user_id=417`）当日整体命中率约 48%，明显低于其 8/10–8/13 的 73%–91%。整体数字下降通常有两种形态：全局劣化，或若干维度被拉低后的加权平均。按请求入口拆开后属于后者。

| 8/17 入口 | 请求数 | prompt | 缓存 | 命中率 |
| -- | --: | --: | --: | --: |
| `/v1/chat/completions` 直打 `deepseek-v4-flash-0731`（Reasonix） | 444 | 5669 万 | 4744 万 | 83.67% |
| `/v1/messages` → `llm-prime`（Claude Code） | 451 | 4811 万 | 687 万 | **14.29%** |
| `/v1/chat/completions` → `llm-prime` | 7 | 132 万 | 19 万 | 14.31% |

两条链路使用同一 channel、同一 token、同一上游模型，`llm-prime` 只做模型名映射。回溯更早日期，该形态可复现，不是偶发抖动：8/14 首次大量使用 Claude Code 当天，`/v1/messages` 为 15.96%，而同日同模型的 `/v1/chat/completions` 仍有 93.11%。

### 2.2 定位过程

**第一步，看分布而不是均值。** 对 8/17 的 451 次 `/v1/messages` 请求按缓存长度分桶，结果高度反常：

| 缓存 token | 请求数 | 含义 |
| -- | --: | -- |
| 0 | 28 | 完全冷启动或被挤出 |
| 256 | 40 | 仅剩模板级极短前缀 |
| 恰好 17920 | **383** | 固定前缀命中，之后全部 miss |

当天 prompt 从 4 万涨到 18 万，缓存长度却纹丝不动地钉在 17920。这排除了"用户内容太碎"或"KV 争抢"这类概率性解释——概率性因素不会产生一个精确常数。

**第二步，从常数反推断点位置。** 17920 = 280 × 64，DeepSeek 的 block 按 64 token 对齐，说明这是一个真实的、稳定的前缀命中。Claude Code 的 23 个工具定义 JSON 约 7.4 万字符，按约 4 字符/token 估算约 1.85 万 token，与 17920 吻合。据此可以判断：请求模板中排在工具定义之后的第一段内容，每一轮都在变。平均 prompt 约 10.7 万，17920 / 106667 ≈ 14.3%，与当日实测的 14.29% 完全一致，反推链条闭合。

**第三步，抓转换前的原始请求，对各段取哈希。** 从 Langfuse `events_full` 取出原始 Claude 请求，对 `system` 三块和 `tools` 分别做 sipHash64：

| 块 | 内容 | 8/17 全天稳定性 |
| -- | -- | -- |
| 0 | `x-anthropic-billing-header: cc_version=2.1.116.d8c; cc_entrypoint=claude-vscode; cch=<5 位十六进制>;` | **每请求唯一** |
| 1 | `You are Claude Code, Anthropic's official CLI...` | 同一哈希 `88D95ABDDD0F5572` |
| 2 | 主系统提示（工作区、分支、git status 快照等） | 同一哈希 `363BFB9E19A6BCC3` |
| tools | 23 个工具定义 | 同一哈希 |

连续几拍的 `cch` 取值分别为 `8c7f0`、`7ee55`、`490dc`，无规律。块 2 虽然包含 git status，但 Claude Code 在会话内不刷新它，当天哈希未变，因此"git status 每轮在变"这一常见猜测也被排除。

**第四步，确认转换层行为。** `/v1/messages` 转 OpenAI Chat 时，非 OpenRouter 路径把所有 system 段顺序拼成一条 string：

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

实际发给 H200 的 system 因此以这段头开始：

```text
x-anthropic-billing-header: cc_version=2.1.116.d8c; cc_entrypoint=claude-vscode; cch=XXXXX;You are Claude Code...
```

### 2.3 根因

这段头是 Claude Code 给 Anthropic 官方账单用的探测字段，new-api 与本地 DeepSeek 都不读取（全库检索无该字段名）。问题不在于它存在，而在于它每请求变化，且经拼接后落在了上游 prompt 的最前端。

vLLM 的前缀缓存是链式哈希：block N 的 key 由 block N-1 的 key 与本块内容共同决定。首块内容一变，其后所有 block 的哈希随之改变。`cch=` 位于 token 0 附近，因此同一会话的下一轮请求既无法复用上一轮的 system，也无法复用上一轮的对话历史。只有排在这段头之前的工具定义仍能命中，这就是那个精确的 17920。

作为对照，Reasonix 的 system 以固定的 `You are Reasonix, a coding agent.` 开头，327 次以上请求的前 500 字节几乎不变，缓存随对话长度增长到 12 万以上，命中率因此维持在 80% 以上。

补充两项次要因素，二者均不构成根因：当天 Claude 会话几乎全部是 compact 续聊，compact 会整段改写第一条 user 消息，这解释了压缩周期边界处的冷启动，但解释不了周期内每一轮都 miss；09:00–14:00 Claude 与 Reasonix 并发打同一块 H200 存在 KV 争抢，但 Reasonix 在同样争抢下仍能维持高命中，说明争抢不是决定性因素。

### 2.4 处置

new-api 原本没有"丢弃指定 system 前缀"的能力。`system_prompt_override` 只能向前插入，`RemoveDisabledFields` 只处理顶层 JSON 键，`param_override` 的 `regex_replace` 可以workaround但不是产品功能。

最终实现为渠道级开关 `strip_anthropic_billing_header`，默认关闭。设计上做了几处收敛：

- 只在 Claude Messages → 非 Claude 上游、且把 system 压成一条 string 的路径生效。OpenRouter 的 Claude 分块路径不过滤，因为那条链路上的头可能被真正的 Anthropic 端识别。
- 识别规则写死：text 块 `TrimSpace` 后以 `x-anthropic-billing-header:` 开头则整块丢弃；字符串形态的 system 则去掉第一行。不做成用户可填的通用前缀编辑器——目前只有这一个已知的、每轮都变且排在最前的头，通用能力会带来更大的误伤面。
- 不修改 `ClaudeRequest.System` 原文，因此预扣估算与 Langfuse 原始抓包仍能看到这段头。
- 不改计费公式，不影响走 `/v1/chat/completions` 的其他客户端。
- 回滚只需关闭渠道开关，不必回滚镜像。

上线后在 channel 5 打开。

### 2.5 效果验证

验证的前提是确认用法没变，否则前后对比不成立。8/18 当日 Langfuse 记录显示：Claude Code 仍走 `/v1/messages`，每个请求的 `cch` 仍在变化，块 1/2 哈希与 8/17 相同，会话仍然全部是 compact 续聊。也就是说，唯一变量是网关剥掉了这段头。

| | 请求数 | 命中率 | 缓存钉在 17920 的次数 |
| -- | --: | --: | --: |
| 8/17（修复前） | 451 | 14.29% | 383 |
| 8/18（修复后，截至回看时） | 54 | **83.09%** | **0** |

缓存长度分布也从"单点钉死"变回正常形态：5 次 0、4 次 256、3 次不足 64k、14 次 64k–128k、28 次 128k 以上。高桶中 `avg_cache ≈ avg_prompt`，说明命中的是对话前缀而非仅工具定义。

同一小时内连续几轮的实际数据（上海时间）：

```
09:28  prompt 109974 → cache    256   0.2%   冷启动
09:28         111232 →      109824  98.7%
09:28         111422 →      111104  99.7%
09:36         118702 →           0   0%     间隔较久，KV 被挤出后再冷
09:37         119455 →      118528  99.2%
```

需要说明的是 8/18 样本仅 54 次请求，为部分日数据。但结合分布形态、连续轮次曲线以及"钉死次数归零"三项证据，可以认定行为已经改变，而不是样本波动。另一位仅使用 Claude Code、同样携带该头的用户当日命中率约 91%，可作旁证。

剩余未达 95% 以上的部分，主要来自多客户端并发、超长上下文把 GPU KV 挤爆后的整段冷启动，占请求约 15%。这一部分本应由二级缓存兜底，见下一节。

---

## 三、问题二：二级 KV 缓存只写不读

### 3.1 现象

compose 中配置了 `OffloadingConnector`，约 960 GB pinned 主机内存，设计意图是 GPU 池放不下的前缀在命中时从内存 DMA 回显存。

对容器自 08-07 02:48 启动至记录日的完整日志做只读计数（`docker logs | rg -c`，未改动运行中进程）：

| 模式 | 命中行数 |
| -- | --: |
| `kv_offload_store_bytes`（写入） | 198422 |
| `kv_offload_load_bytes`（回读） | **0** |
| `External prefix cache hit rate: 0.0%` | 413174 |
| `External prefix cache hit rate` 非 0.0% | **0** |

11 天，近 20 万次写入指标点，零次回读。

### 3.2 排除配置缺失

启动参数确认 L2 是开启的（`08-07 02:48:01`）：

```text
kv_transfer_config=KVTransferConfig(
  kv_connector='OffloadingConnector',
  kv_role='kv_both',
  kv_connector_extra_config={'cpu_bytes_to_use': 960000000000},
)
disable_hybrid_kv_cache_manager: False
speculative_config: {'method': 'dspark', 'num_speculative_tokens': 7, ...}
block_size: 256
enable_cumem_allocator: True
```

`expandable_segments` 与 connector 的互斥已通过 `--enable-cumem-allocator` 解开，KV 页物理地址稳定才能 pin 给 DMA，这部分是正确的。8 个 worker 与 EngineCore 均创建了 CPU offload（`02:50:05` / `02:54:45`），写入每秒数 GB，说明 `cpu_bytes_to_use` 确实生效。改用 `--kv-offloading-size` 也无济于事，那只是同一套 connector 的另一个入口，lookup 语义相同。

### 3.3 指标口径澄清

这个问题此前之所以没被发现，一部分原因是指标容易被误读。`cpu_cache_usage_perc` 经常掉回 0，看上去像"内存池是空的"，但这个 gauge 的定义是**正在 pin 的传输**占池子的比例，写完 `complete_store` 的驻留块会转为 evictable，按设计不计入。因此写完立刻归零是正常现象。

判断"有没有回读"的正确判据只有两个：`kv_offload_load_bytes` 是否大于 0，以及 `External prefix cache hit rate` 是否非 0。

### 3.4 排除"时机不对"

一种自然的怀疑是：GPU 一直有空间，所以还没到该走 L2 的时候。这个假设在 L1 已命中的时刻成立，但在下面这种时刻不成立。取记录日 `08-18 09:25` 附近的日志：

```text
09:25:22  GPU KV 0.5%,  Prefix 88.3%, External 0.0%
09:25:24  prompt throughput 34844 tok/s   （大量预填充正在发生）
          Prefix 88.3%, External 0.0%
          store_bytes=20646236160
09:25:30  Prefix 88.5%, External 0.0%
```

GPU KV 占用仅 0.5%、同时有 3.4 万 tok/s 的预填充在跑，正是 L2 最该出手的时刻，External 依然是 0.0%。启动后第一分钟的日志形态也一样：`lookup_sync_delay_seconds_count` 有计数，说明 lookup 被调用过，只是结果全是 miss。

### 3.5 根因

生产挂载的 `patch/scheduler.py` 中，`_lookup` 先查全量 MLA 组，再逐个查滑动窗口组与 eagle 组：

```641:642:patch/scheduler.py
                if num_hit_chunks == 0:
                    return 0
```

任意一组零命中，整次 L2 load 直接放弃，已经写到 CPU 的 MLA 前缀也不会被 promote。调度器侧将该结果记入 External 统计（`hits = num_external_computed_tokens`），lookup 返回 0，External 便恒为 0%。

而 DeepSeek-V4-Flash + DSpark 的 KV 结构恰好必然触发这个否决。模型 `config.json` 中 `sliding_window=128`，`compress_ratios` 含大量 4 / 128，`dspark_target_layer_ids=[40,41,42]`，vLLM 据此拆出多组 KV：

| Group | 内容 | block 大小 | 落盘行为 |
| -- | -- | -- | -- |
| 0 | 全量 MLA | 256 | 正常 store，即那几十 GB/s 的写入 |
| 1+ | compressor 滑动窗口（C4 / C128） | 4 或 8 | 每个 256-token 段只保留尾部，前段按设计跳过 |
| 2 | DSpark/MTP，启动时标记为 eagle | 同上 | **decode 阶段故意不存最后一块** |

最后一条在启动日志里写得很明确：

```text
[scheduler.py:194] KV offloading: EAGLE/MTP draft attention groups [2] detected.
  The trailing chunk of these groups will be excluded from offloading due to volatility.
```

续聊请求的前缀 = 旧 prompt + 上一轮 decode 出来的 assistant token。eagle 组的尾块按设计没有落盘，lookup 从尾部往前匹配必然对不上，于是 `return 0`，MLA 那一大坨已写入的数据随之作废。这与"缓存过期"无关：即使 CPU 中的 MLA 数据完好，调度器也不接受只加载 MLA。

### 3.6 处置决策

**本次不在生产上关闭 L2，也不热改 vLLM。** 定位清楚即可，修改 lookup 要动调度器，不适合在业务时段碰这套 8 卡服务。

两条后续路径，可二选一或并行：

1. 等上游修复。诉求是 OffloadingConnector 在混合 KV 场景下，SWA/eagle miss 时不应否决已命中的 MLA。
2. 自行修改同一处 lookup，miss 时仍 promote 全量 MLA，SWA 部分现场重算。

修复成功的判据：日志中出现 `kv_offload_load_bytes > 0`；且在 GPU 占用很低时喂入一条刚被挤出的长前缀，External 不再是 0%；new-api 侧同类长会话的 `cache=0` 整段 miss 应明显下降。

在此之前，这 960 GB pinned 内存对命中率没有任何贡献，只消耗宿主机内存与 PCIe 写带宽。若决定先卸掉，关闭 OffloadingConnector 不会让 L1 变差。

---

## 四、问题三：回包 usage 口径与上下文压缩时机

这一项与前两项不在同一层，但用户观感是绑定的，因此一并记录。

OpenAI 兼容上游（包括本地 DeepSeek）习惯把缓存命中计入 `prompt_tokens`。网关转成 Anthropic Messages 时若原样写入 `input_tokens`，Claude Code 会把整段已命中的上下文当作"新输入"。流式场景还多一层放大：`message_start` 会先带一版预估 prompt，客户端取它与终态 usage 的较大值，而预估值不含缓存信息。

Claude Code 的自动压缩正是读取这套 `input_tokens`。数值被抬高，客户端就以为窗口更满，从而比真实上下文更早触发 compact。compact 之后整段历史被摘要替换，前缀全部改变，L1 再好也接不上——这构成一个自我强化的劣化环。

8/13 合入的渠道开关 `anthropic_messages_exclude_cache`（channel 5 已开）只修改回给客户端的 usage：

- 终态 `input_tokens = prompt_tokens − cache_read − cache_creation`，下限 0，缓存部分单独放在 `cache_read_input_tokens`
- 流式 `message_start` 不再发送预估值，避免取 max 抬高

计费与网关后台日志不变，仍使用上游原始 usage。

值得说明的是这一项与问题一的依赖关系：在计费头问题修复之前，真实 `cache_read` 几乎为零，扣不扣区别不大，所以当时看不出效果。修复后命中率到 80% 以上，若不扣除，客户端会把十几万已命中 token 当作新输入，压缩会明显提前。两项修改必须同时到位才有意义。

---

## 五、影响

| 项 | 修复前 | 现状 / 下一步 |
| -- | -- | -- |
| Claude Code 前缀命中 | 每轮改写首块，仅约 1.8 万工具 token 能命中，整日 14% | 会话内 98%–99%，整日约 83% |
| 长会话被挤出 GPU | 只能整段重算，L2 从未回读 | 根因已定位，等上游或自行修改 lookup；在此之前并发多条 20 万级会话仍会有冷启动 |
| 回包 usage | 含缓存 + 流式预估取 max，用量虚高、压缩偏早 | 按 Anthropic 语义扣除，压缩按真实新输入触发 |

对日常使用 Claude Code 的同事：同一会话内续聊应明显更快，因为省掉了大部分预填充；也不应再频繁出现"上下文已满，先总结一下"。如果仍然频繁压缩，更可能是会话确实很长，或同时开了多个 Agent 把 GPU KV 挤满，而不是网关又把数字报错了。

关于成本，需要澄清一点：当前套餐扣费表达式为 `len <= 256000 ? p * 3 + c * 3 : p * 10 + c * 10`，其中不含缓存项 `cr`。因此命中率提升节省的是 **H200 的预填充算力**，而不是自动少扣用户额度。剥头本身只让每轮 prompt 少约 20–30 token，可忽略。若今后希望"命中缓存则少扣套餐"，需要单独修改计费表达式，与本次两个开关无关。

---

## 六、结论与后续

问题一已闭环。这个案例的可复用之处在于定位路径：整体指标下降先按维度拆分而不是直接归因；看分布而不是均值；出现精确常数时优先从常数反推结构而不是找概率性解释；最后用转换前的原始请求做哈希对照来锁定唯一变量。整条链路上真正的排查成本集中在第三步，而它之所以可行，是因为 8858 的 Langfuse 从 8/15 起开始记录完整请求结构——8/14 其实已经出现过同一症状，当时因为没有抓到请求正文而未能定位。

问题二未修复，根因清晰，属于框架层限制而非配置错误，后续走上游或自改两条路径之一。

问题三已随问题一一并生效。

明确不做的事项：不在业务时段重启或热补 vLLM；不把"去头"做成用户可填的通用前缀编辑器；不为提升命中率而改动计费表达式。

---

## 附录 A：复查方法

命中率按 token 计算，样本用户 `user_id = 417`：

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

判断问题一的开关是否仍在起作用：`/v1/messages` 的 `pinned_17920` 应接近 0；命中率应与同日 `/v1/chat/completions` 处于同一量级。注意 Langfuse 中原始 Claude `system[0]` **仍会**出现 `x-anthropic-billing-header`，因为抓取发生在转换之前，这不表示开关失效。

渠道设置确认：

```sql
SELECT id, name, setting FROM channels WHERE id = 5;
```

`setting` 中应包含 `"strip_anthropic_billing_header": true`。

复查问题二只需只读容器日志，不要重启：

```bash
# 是否出现过成功回读（修好之前应为 0）
docker logs deepseek-v4-flash-vllm 2>&1 | rg -c 'kv_offload_load_bytes'

# External 是否仍恒为 0.0%
docker logs --since 5m deepseek-v4-flash-vllm 2>&1 | rg 'External prefix cache hit rate'
```

## 附录 B：相关记录

更详细的调查笔记（含完整 SQL、日志片段、代码锚点）在 new-api 仓库：

- [缓存优化目录](README.md)
- [计费头打断前缀缓存](2026-08-18-claude-code-billing-header.md)
- [vLLM OffloadingConnector 只写不读](2026-08-18-vllm-l2-offload-write-only.md)
- 回包口径改动台账：[feat-anthropic-messages-cache-usage](../patches/feat-anthropic-messages-cache-usage.md)
- 剥头改动台账：[feat-strip-claude-system-prefix](../patches/feat-strip-claude-system-prefix.md)
