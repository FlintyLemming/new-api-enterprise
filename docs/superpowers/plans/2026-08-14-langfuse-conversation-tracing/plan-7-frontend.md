# Plan 7 — Langfuse 设置 UI 与七语言 i18n

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) 或 superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 设计文档 §12 与 §16.8——System Settings 中新增 Langfuse section：启用开关 + 首次/重新启用确认步骤（显式选择 Sample Rate 与 Send Content）、Host/Keys/Environment、容量提示（reservation/slots/RPS 估算）、session header/body path 配置与预设、高级 queue/batch/flush；全量校验镜像后端；7 语言 i18n。

**Architecture:** 放在 `web/src/features/system-settings/integrations/`（与 `monitoring-settings-section.tsx` 同级），注册进该目录的 section registry（实现时确认：监控 section 的注册文件在哪个 registry——`operations/section-registry.tsx` 或 integrations 自己的注册点；Langfuse 属于集成类，跟随 monitoring 的注册方式）。与 monitoring 不同：读写走**专用** `/api/option/langfuse`（GET/PUT），不使用 `useUpdateOption` 通用 hook。

**Tech Stack:** React 19 + TanStack Router、react-hook-form + zod、Base UI/shadcn 组件、sonner toast、i18next。

## Global Constraints

- 见 `00-master.md`。本计划额外约束：
  - **locale 写入只经 `web/scripts/add-missing-keys.mjs`**（临时脚本，跑完删除），禁止直接编辑 `web/src/i18n/locales/*.json`；必须先加载 `.agents/skills/i18n-translate/SKILL.md` 并按其流程执行（即使 skill loader 不可用，也按 §12 写死的步骤走）。
  - UI 不展示、不回传任何请求中采集到的 raw session 值。
  - Secret Key 只显示"已配置/未配置"状态；提交时空值=保留、显式勾选 clear=清除（仅禁用状态允许）。
  - `sample_rate=0` 是合法显式选择（"保留配置、暂停采集"），确认步骤不得阻止提交 0。

---

### Task 1: API client 与类型

**Files:**
- Create: `web/src/features/system-settings/integrations/langfuse-api.ts`
- Test: `web/src/features/system-settings/integrations/langfuse-api.test.ts`（若仓库无该层测试惯例，并入 Task 3 组件测试）

**Interfaces:**
- Produces:

```ts
export interface LangfuseSettingsView {
  enabled: boolean; host: string; public_key: string; secret_key_configured: boolean;
  environment: string; sample_rate: number; send_content: boolean;
  max_content_bytes: number; max_response_bytes: number; max_in_flight_capture_bytes: number;
  max_session_body_bytes: number; session_header_names: string[]; session_body_paths: string[];
  queue_size: number; batch_size: number; flush_interval_seconds: number;
}
export interface LangfuseSettingsUpdate extends Omit<LangfuseSettingsView, 'secret_key_configured'> {
  secret_key: string;        // "" = keep
  secret_key_clear: boolean; // 仅 enabled=false 时可 true
}
export async function fetchLangfuseSettings(): Promise<LangfuseSettingsView>
export async function updateLangfuseSettings(payload: LangfuseSettingsUpdate): Promise<LangfuseSettingsView>
```

实现照现有 api client（`web/src/api/` 或 feature 内的请求封装——跟随 monitoring section 的数据获取方式，把 URL 换成 `/api/option/langfuse`）。

- [ ] **Step 1–3: TDD（先写 fetch/update 的 mock 测试，再实现）→ 跑绿 → Commit**

```bash
git add web/src/features/system-settings/integrations/langfuse-api.ts
git commit -m "feat(web): langfuse settings api client"
```

---

### Task 2: 校验 schema（镜像后端）

**Files:**
- Create: `web/src/features/system-settings/integrations/langfuse-schema.ts`
- Test: `web/src/features/system-settings/integrations/langfuse-schema.test.ts`

**Interfaces:** `langfuseFormSchema`（zod）+ `validateEnableTransition(persisted: LangfuseSettingsView, next: 表单值): string | null`。

规则与 plan-2 Task 2 的表逐条镜像：host 绝对 http/https、无 userinfo/query/fragment、不得以 `/api/public/otel/v1/traces` 结尾；enabled 时 public key 非空（secret 由后端校验，前端仅提示"未配置需输入"）；environment `^[a-z0-9_-]{1,40}$` 且非 `langfuse` 前缀；sample_rate [0,1]；各 size/queue/batch/interval 上下限（同 plan-2 常量，含 flush 1–300）；`batch<=queue`；`2*content+response <= max_in_flight_capture_bytes`；`(queue+3*batch)*(2*content+response) <= 256MiB` 且 `2*content+response <= 9_000_000`；header 名合法且非凭证头；body path 非空无空白。**启用转换**：persisted.enabled=false 且 next.enabled=true 时 `sample_rate_confirmed`/`send_content_confirmed` 必须为 true（见 Task 3 确认步骤），否则返回错误 key。

- [ ] **Step 1–3: 表驱动 TDD（正负例全覆盖上表；边界 tuple 用 plan-2 的"单项上限可达"四组镜像）→ 跑绿 → Commit**

```bash
git add web/src/features/system-settings/integrations/langfuse-schema.ts web/src/features/system-settings/integrations/langfuse-schema.test.ts
git commit -m "feat(web): langfuse settings validation mirroring backend"
```

---

### Task 3: Section 组件与注册

**Files:**
- Create: `web/src/features/system-settings/integrations/langfuse-settings-section.tsx`
- Modify: 对应 `section-registry`（跟随 monitoring 的注册文件）
- Test: `web/src/features/system-settings/integrations/langfuse-settings-section.test.tsx`

**控件清单（§12，全部 `t()`）：**
1. 启用 Switch；开启时（首次或从关闭重新开启）进入**确认步骤**面板：必须显式操作 Sample Rate 控件与 Send Content Switch（`sample_rate_confirmed`/`send_content_confirmed` 状态位，操作过后置 true）才允许保存；面板文案含"采样会丢弃未命中的 trace""启用 Send Content 后 prompt 与模型响应将发送至外部 Langfuse 实例""rate 0 = 保留配置、暂停采集"。提交时发送 presence-aware：确认步骤中把 `sample_rate`/`send_content` 一定带上。
2. Host（placeholder `https://langfuse.example.com`）、Public Key、Secret Key 密码框（后缀显示 已配置/未配置 徽标；clear 勾选仅在 enabled=false 时可见）。
3. Environment、Sample Rate（number 0–1）、Send Content Switch。
4. input/output 内容上限（max_content_bytes）、response 捕获上限（max_response_bytes）、全局捕获预算（max_in_flight_capture_bytes）、session body 完整读取上限（max_session_body_bytes）——数字输入。
5. **容量实时提示**（随当前值计算展示，纯展示不提交）：`reservation = 2*max_content_bytes + max_response_bytes`、`capture_slots = floor(budget/reservation)`、`sampled RPS ≈ slots / 平均捕获生命周期秒数`（默认按 30s 展示例值）、正文常驻 ≈ `budget + queue*reservation`、导出阶段 ≈ `budget + (queue+3*batch)*reservation` 的 MiB 规划值；文案注明"估算值，不按实际响应增量计费，不替代整进程内存规划"。默认值显示 约 640 KiB / 819 slots / ~27.3 sampled RPS / ~552 MiB / ~582 MiB。
6. 额外 session Header 名（多值输入）；session body path 多值输入 + **预设复选**：`metadata.session_id`、`metadata.conversation_id`、`conversation_id`、`chat_id`（默认均不选；勾选只把精确 path 加入列表）；附近提示"无有效 Header session 的请求将产生最多 64 KiB 的完整 JSON 身份读取"。
7. 折叠的高级区：queue_size、batch_size、flush_interval_seconds。
8. 保存按钮 → PUT 成功 toast + 刷新视图；校验失败就地展示 zod 错误；后端 400 透传展示。

session 作用域说明文案："客户端 session 值按 New API 用户作用域导出为 `{userId}:{raw}`，相同 raw 值只会在同一用户内聚合；没有正数用户 ID 时省略。"

**测试（§14.4）：**
- 未确认（缺任一显式选择）时保存被阻止；两者都操作后可提交且 payload 含两字段；rate 0 可提交。
- 预设默认不选；勾选后 `session_body_paths` 恰含该精确 path。
- secret：已配置视图 + 空输入提交 → payload `secret_key:""`；clear 勾选在 enabled=true 提交时被 schema 拒绝。
- 容量提示随输入变化（断言计算函数输出的字符串/数值）。

- [ ] **Step 1–3: 组件 TDD（渲染 + 交互，照 monitoring section 测试的测试环境）→ `bun run typecheck` 跑绿 → Commit（不含 locale）**

```bash
git add web/src/features/system-settings/integrations/
git commit -m "feat(web): langfuse settings section with enable confirmation and capacity hints"
```

---

### Task 4: 七语言 i18n

- [ ] **Step 1**: 读 `.agents/skills/i18n-translate/SKILL.md`；`cd web && bun run i18n:sync`；读 `src/i18n/locales/_reports/_sync-report.json` 确认新增 key 列表（en 之外六语言 + 可能的 en 缺失）。
- [ ] **Step 2**: 创建 `web/scripts/add-missing-keys.mjs`（skill 模板），`newKeys` 内为全部新增英文 key 提供 `en/zh/zh-TW/fr/ja/ru/vi` 七语言值（zh 先译，其余语言按 skill 规则：品牌名/URL/字段名保留英文，`{{var}}` 占位保留）；运行 `node scripts/add-missing-keys.mjs`。
- [ ] **Step 3**: `node scripts/find-missing-keys.mjs`（或等价检查）确认 "All t() keys found"；再次 `bun run i18n:sync`；**删除两个临时脚本**。
- [ ] **Step 4**: `cd web && bun run typecheck && bun run lint（涉及文件） && bun run build` 全绿后 Commit：

```bash
git add web/src/i18n/locales/
git commit -m "feat(web): langfuse settings i18n for seven locales"
```

---

## Self-Review 已核对

- §12 控件清单逐项有落点；确认步骤/presence-aware/rate 0/预设默认不选/容量提示数值（640KiB、819、27.3、552/582MiB）均与设计一致；§14.4 四项验证对应 Task 2/3/4 + build；i18n 流程严格走 skill 规定的脚本路径。
