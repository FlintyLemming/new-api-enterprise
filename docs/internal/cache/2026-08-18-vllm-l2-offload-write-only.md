# vLLM OffloadingConnector（L2）只写不读

| 项 | 值 |
| -- | -- |
| 日期 | 2026-08-18 |
| 环境 | H200 × 8，`deepseek-v4-flash-vllm`，compose [`docker-compose.vllm.yml`](/mnt/extend/models/llm/deepseek-ai/DeepSeek-V4-Flash-0731/docker-compose.vllm.yml) |
| 镜像 | `vllm/vllm-openai:v0.26.0`，挂了 `./patch/scheduler.py`（PR #49146 abort 崩溃修复） |
| 模型 | DeepSeek-V4-Flash-0731，`--speculative-config method=dspark`，`--no-disable-hybrid-kv-cache-manager` |
| 观测 | 容器日志（`08-07 02:48` 启动后一直跑到记录日），**只读**，未改运行中进程 |
| 对照代码 | `/home/flintylemming/Projects/reference/vllm` + 生产挂载的 `patch/scheduler.py` |
| 结论 | **未解决。** 不是 compose 没开 L2，是 OffloadingConnector 在 DSV4+DSpark 混合 KV 上 lookup 过严：MLA 已经写入 CPU，SWA/eagle 任一 miss 就把整次 load 否决。External hit 从启动到现在一直是 0%。 |

和 [计费头那篇](2026-08-18-claude-code-billing-header.md) 的关系：那篇修的是「发给 GPU 的前缀每轮都变」，L1（GPU prefix cache）因此钉死在约 14%。本篇是 L1 修好之后，**GPU 被挤出的那一截本该从 CPU 回读，但从未回读**。两件事叠在一起，才会看到孔华去掉 `cache=0` 后 95%、含冷启动只有 80%。

---

## 1. 预期 vs 现象

compose 注释写的设计：

> 二级 KV cache：完成的 KV block 异步 DMA 到 pinned host memory，GPU 池放不下的前缀在命中时按需 promote 回 GPU。

也就是：

1. 请求算完 → 把 block 写到 CPU（L2）
2. GPU LRU 把这块挤掉（看起来像「过了几分钟没人用」）
3. 同一前缀再来 → 从 CPU DMA 回 GPU，`cached_tokens` 仍应接近 prompt

实际：第 1 步一直在发生，第 3 步从来没有。

没有「5 分钟 TTL」这个旋钮。GPU / CPU 都是 LRU。5 分钟只是生产上前缀被挤出 GPU 的经验时间。

---

## 2. 配置并没有配错

启动参数里 L2 是开着的（`08-07 02:48:01`）：

```text
kv_transfer_config=KVTransferConfig(
  kv_connector='OffloadingConnector',
  kv_role='kv_both',
  kv_connector_extra_config={'cpu_bytes_to_use': 960000000000},
  ...
)
disable_hybrid_kv_cache_manager: False
speculative_config: {'method': 'dspark', 'num_speculative_tokens': 7, ...}
block_size: 256
enable_cumem_allocator: True
```

`expandable_segments` 与 connector 的互斥用 `--enable-cumem-allocator` 拆开了：KV 页物理地址稳定，才能 pin 给 DMA。这块是对的。

启动后 8 个 worker + EngineCore 都建了 CPU offload（`08-07 02:50:05` / `02:54:45`）：

```text
[factory.py:47] Creating offloading spec with name: CPUOffloadingSpec
[scheduler.py:194] KV offloading: EAGLE/MTP draft attention groups [2] detected.
  The trailing chunk of these groups will be excluded from offloading due to volatility.
```

GPU 池当时是 **6,502,918 tokens**（`kv_cache_utils.py:2177`）。L2 容量按 compose 是 960 GB 合计、按 world_size=8 均分。

`--kv-offloading-size` 只是同一套 OffloadingConnector 的马甲，换成它也改变不了 lookup 语义。

---

## 3. 日志：写很大，读为零

### 3.1 启动后第一分钟

`08-07 02:55:01` 起，几乎每一秒都在 store，从未 load：

```text
02:55:01  Prefix cache hit rate: 0.0%, External prefix cache hit rate: 0.0%
          kv_offload_store_bytes=25058119680   (~23.3 GiB)
          kv_offload_cpu_cache_write_usage_perc=0.017
          kv_offload_cpu_cache_read_usage_perc=0.0

02:55:02  store_bytes=22903879680
02:55:03  store_bytes=19026247680
...
02:55:07  GPU KV usage 1.4%, Prefix 0.0%, External 0.0%
          store_bytes=12106828800
          usage_perc 掉回 0.0（写完 unpin，不是缓存被清空）

02:55:08  Prefix cache hit rate: 13.3%, External 0.0%
          lookup_sync_delay 有计数（查过 L2），但仍无 load_bytes
```

同一秒里：GPU 前缀从 0% 爬到 13%（L1 开始工作），External 仍是 0%。lookup 被调用了（`lookup_sync_delay_seconds_count≥1`），只是结果全是 miss。

### 3.2 记录日（11 天之后）仍一样

`08-18 09:25` 附近，L1 已经稳定在 88%，L2 还是 0：

```text
09:25:22  GPU KV 0.5%,  Prefix 88.3%, External 0.0%
          store 尚未发生，read_usage=0.0

09:25:23  store_bytes=6497187840, store_time=0.376s
          write_usage=0.0097, read_usage=0.0

09:25:24  prompt throughput 34844 tok/s  （有大 prefill，正是该回读的时候）
          Prefix 88.3%, External 0.0%
          store_bytes=20646236160

09:25:30  Prefix 88.5%, External 0.0%
          store_bytes=12684165120
```

对启动以来的完整日志做了一次只读计数（`docker logs | rg -c`，未改容器）：

| 模式 | 命中行数 |
| -- | --: |
| `kv_offload_store_bytes` | **198422** |
| `kv_offload_load_bytes` | **0** |
| `External prefix cache hit rate: 0.0%` | **413174** |
| `External prefix cache hit rate:` 且不是 0.0 | **0** |

从 `08-07 02:55` 到记录日，L2 **写了近 20 万次指标点，回读一次都没有**。External hit 累计是 0.0%，不是「偶尔漏一次」。

### 3.3 不要被 usage=0 骗到

`CPUOffloadingSpec` 对三个 gauge 的说明：

| 指标 | 实际含义 |
| -- | -- |
| `cpu_cache_usage_perc` | **正在 pin 的传输**占池子的比例。写完/读完的驻留块不算 |
| `cpu_cache_write_usage_perc` | 尚未 `complete_store` 的写入 |
| `cpu_cache_read_usage_perc` | 尚未 `complete_load` 的读回 |

所以写完立刻 `usage=0` 是设计如此，只说明此刻没有 in-flight DMA。判断「有没有读」看 `load_bytes` 和 External hit。

---

## 4. 原因

### 4.1 Lookup 是全组 AND

生产挂载的 `patch/scheduler.py`：

```641:642:patch/scheduler.py
                if num_hit_chunks == 0:
                    return 0
```

`_lookup` 先查全量 MLA，再查每一个 sliding-window / eagle 组。**任意一组 0 命中，整次 L2 load 放弃**，已经命中的 MLA 前缀也不会 promote。

调度器侧（`v1/core/sched/scheduler.py`）把这次结果记进 External 统计：`hits = num_external_computed_tokens`。lookup 返回 0 → External 永远 0%。

### 4.2 DSV4 + DSpark 正好对不齐

`config.json`：`sliding_window=128`，`compress_ratios` 里大量 `4` / `128`，`dspark_target_layer_ids=[40,41,42]`。

vLLM 会拆成多组 KV：

| Group | 内容 | block | 备注 |
| -- | -- | -- | -- |
| 0 | 全量 MLA | `--block-size 256` | store 正常，这就是那几十 GB/s 的写入 |
| 1+ | compressor SWA（C4 / C128） | **4 或 8** token/block，window 8 或 128 | 每个 256-token 段里只存尾巴，前面的按设计跳过 |
| 2 | DSpark/MTP，启动时标成 eagle | 同上 | **decode 故意不存最后一块**（`groups [2] ... trailing chunk excluded due to volatility`） |

续聊前缀 = 旧 prompt + 旧 assistant（decode 出来的 token）。eagle 组最后一块按设计没落盘 → SWA/eagle lookup 从尾巴往前数对不上 → `return 0` → MLA 那一大坨 CPU 数据作废。

这和「5 分钟过期」无关：就算 CPU 里 MLA 还在，调度器也不肯只 load MLA。

### 4.3 和孔华 80% 的关系

[上一篇回看](2026-08-18-claude-code-billing-header.md) 之后：

- 去掉 `cache=0`：孔华 94.6%，和 90% 档同一水平 → L1 前缀已经稳
- 含冷启动：79.9%，冷启动占请求 15%
- 冷启动里大部分是 **≥80k 的整段 miss**（opencode / Reasonix 长会话被挤出 GPU）

这些整段 miss 正是 L2 该出手的时刻。External 仍是 0%，所以它们全部在 GPU 上重算。L2 现在只是在烧 PCIe 写带宽和 1 TiB 级 pinned 内存，**对命中率没有贡献**。

---

## 5. 排除项

| 假说 | 为何排除 |
| -- | -- |
| `cpu_bytes_to_use` 没生效 | worker/EngineCore 都创建了 `CPUOffloadingSpec`，store 每秒数 GB |
| `expandable_segments` 把 pin 弄坏了 | 启动会 ValidationError；现在用了 CuMemAllocator，能写就说明页是稳的 |
| 5 分钟 TTL 没配 | OffloadingConnector 没有这个参数 |
| usage=0 表示 CPU 池是空的 | 指标定义就是 in-flight pin |
| GPU 还在所以不该走 L2 | 对「L1 已命中」成立；对「GPU 0.5% + 3 万 tok/s prefill」不成立，那种秒仍 External 0% |
| 换成 `--kv-offloading-size` 就好 | 内部还是 `OffloadingConnector` + 同一套 lookup |

---

## 6. 以后怎么复查

只读容器日志即可，不要重启：

```bash
# 有没有过一次成功回读（应为 0，直到框架修好）
docker logs deepseek-v4-flash-vllm 2>&1 | rg -c 'kv_offload_load_bytes'

# External 是否仍恒为 0.0%
docker logs --since 5m deepseek-v4-flash-vllm 2>&1 \
  | rg 'External prefix cache hit rate'
```

修好的判据：

- 出现 `kv_offload_load_bytes > 0`
- GPU 使用率很低、又来一条刚被挤掉的长前缀时，External hit 不再是 0%
- new-api 里同类长会话的 `cache=0` 整段 miss 应明显下降

---

## 7. 若要修（未做，也不该在生产上现改）

只能改 vLLM，或等上游：

1. **lookup 不要一票否决**：SWA/eagle miss 时仍 promote 已命中的全量 MLA，SWA 现算。
2. 或续聊时允许「只 load MLA」。
3. 现有 `kv_connector_extra_config` 没有这种开关。

在框架修好之前，960 GB pinned L2 **可以视为无效成本**（占宿主机内存、拖慢冷启动、占 DMA）。关掉 OffloadingConnector 不会让 L1 变差，只会少占内存。本篇记录时**没有关**，避免动生产。

---

## 8. 相关文件

- 生产 compose：`/mnt/extend/models/llm/deepseek-ai/DeepSeek-V4-Flash-0731/docker-compose.vllm.yml`
- 生产 scheduler 补丁：同目录 `patch/scheduler.py`（abort 断言，不是本问题的修复）
- 上游 lookup：`vllm/distributed/kv_transfer/kv_connector/v1/offloading/scheduler.py`
- 指标定义：`vllm/v1/kv_offload/cpu/spec.py`、`cpu/manager.py`（`usage` 不含 evictable）
- DSV4 compressor block：`vllm/models/deepseek_v4/compressor.py`（C4 block=4，C128 block=8）
