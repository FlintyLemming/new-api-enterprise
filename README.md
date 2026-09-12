# new-api (internal-custom fork)

本项目 fork 自 [QuantumNous/new-api](https://github.com/QuantumNous/new-api)，是一个 AI API 网关/代理，将 OpenAI、Claude、Gemini、Azure、AWS Bedrock 等 40+ 上游 AI 服务聚合在统一 API 之后，提供用户管理、计费、限流和管理后台。

本分支（`internal-custom`）是**内部部署使用的长期定制分支**，上游的完整介绍、文档、部署指南请直接看[上游仓库](https://github.com/QuantumNous/new-api) 和 [官方文档](https://docs.newapi.ai/)。本 README 只介绍相对上游**改了什么、加了什么**。

- 当前上游基线：`v1.0.0-rc.36`
- 完整改动台账：`docs/internal/`（仅存在于本分支）

## 相对上游的主要改动

### 新增功能

1. **Langfuse OTLP 对话追踪**（可选，默认关闭）
   完整的请求级追踪导出管线：OTLP HTTP exporter、采样、敏感信息脱敏、4 MiB 内容上限、15s 退出排空，按协议聚合输入/输出，在结算点快照真实计费用量并按 audio/text 拆分归因。管理后台提供独立的设置页（含容量提示与开关确认）。内部部署已接入 Langfuse 用于审计与成本分析。

2. **订阅重置卡（Subscription Reset Card）**
   管理员直接向指定用户发放"重置卡"（单次 1–100 张），用户在钱包页自助核销：将名下某个有效订阅的当前周期已用额度清零并重启重置周期。核销走事务内 FIFO 选卡 + 行锁 + CAS 状态翻转，管理端提供独立页面（列表、搜索、批量禁用、软删）。上游的周期重置任务不受影响。

3. **用量统计缓存口径全局归一化开关**
   管理员可选择 log 表 `prompt_tokens` 的落库口径：上游原始 / 统一不含缓存 / 统一含缓存。只影响后台统计与展示，**不触碰计费、客户端响应和 Langfuse 导出**；换算结果与跳过原因写入 `other.admin_info.stats_normalization` 供管理员审计。

4. **Exchange Key 鉴权（内部应用接入）**
   内部应用可用共享 SECRET 派生 per-user HMAC key 直接调用 relay API，无需为每个用户创建和保管独立令牌；服务端只存 HMAC 不存明文，账单归属到真实用户，用户安全设置页可查看调用说明和轮换密钥。用户账单统计、额度扣减等路径均已适配。

5. **每用户并发客户端 IP 数限制**
   滑动窗口内限制单个用户可同时使用的客户端 IP 数量（relay API），支持全局白名单和按用户覆盖，防止账号被多人共享使用。

### 新增渠道开关

6. **剥离 Claude 计费头（strip Claude billing headers）**
   Claude → 非 Claude 协议转换时，可选择在压平 system 文本时剥离 Claude Code 的计费相关头部，保住前缀缓存命中率。

7. **渠道缓存语义声明（cache_prompt_token_semantic）**
   渠道级声明上游原始计费中 prompt 缓存的语义口径，作为计费/遥测前置声明供 Langfuse 追踪与统计使用。

### 修复与改进

8. **日志耗时毫秒精度**
   消费日志记录 `other.duration_ms`（毫秒级），前端展示在秒级 `use_time` 精度不足时回退使用。

## 分支模型

| 分支 | 说明 |
| -- | -- |
| `main` | 上游镜像，不放任何内部改动，只做快进同步 |
| `internal-custom` | 内部部署实际使用，= `main` + 若干内部 patch 的 merge |
| `<type>/<topic>` | 单个改动的开发分支，从 `main` 切出 |

每次同步上游后逐条核对 `docs/internal/README.md` 台账：已被上游收编的 patch 会移除内部副本并归档；有通用价值的改动会整理后往上游提 PR。

## 免责说明

- 本仓库**所有代码均由 AI（Claude Code）生成或辅助完成**，未经完整人工 code review，不保证质量与安全性，使用前请自行评估。
- 本分支的全部修改**仅为公司内部使用方便**，与上游无关，也不代表上游项目的立场。
- 开源本仓库仅为方便他人参考内部实现思路，**并非一个承诺维护的发行版**；不保证持续同步上游、不保证 issue/PR 得到响应。
- 如有功能需求或发现问题，建议直接向[上游项目](https://github.com/QuantumNous/new-api)提 issue/PR；上游才是唯一权威版本。
- 本项目使用需遵守上游许可证（Apache-2.0）及上游的使用合规要求；下游使用者须自行承担全部责任。
