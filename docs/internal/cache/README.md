# 缓存优化记录

8850 生产（new-api + 本地 H200 DeepSeek）上，和前缀缓存有关的问题记录。每篇是一次完整的发现 → 原因 → 处理 → 回看，不是功能设计稿。

代码改动本身记在 `docs/internal/patches/`。这里记的是**现象和验证**。

| 日期 | 标题 | 结论 |
| -- | -- | -- |
| 2026-08-18 | [Claude Code 计费头打断 DeepSeek 前缀缓存](2026-08-18-claude-code-billing-header.md) | 渠道开关剥 `x-anthropic-billing-header` 后，`/v1/messages` 命中率从约 14% 回到约 83% |
| 2026-08-18 | [vLLM OffloadingConnector（L2）只写不读](2026-08-18-vllm-l2-offload-write-only.md) | 不是 compose 配错。DSV4+DSpark 混合 KV 的 lookup 全组 AND，External hit 从 08-07 启动起一直 0% |
| 2026-08-18 | [社内论坛稿：Claude Code × 本地 DeepSeek 缓存报告](2026-08-18-forum-claude-code-cache-report.md) | 初稿，口语体。已由技术报告版替代 |
| 2026-08-18 | [社内技术报告：前缀缓存两起故障的定位与处置](2026-08-18-forum-cache-tech-report.md) | 拟发布版。问题一已闭环，问题二根因已定位待修，问题三随问题一生效 |
