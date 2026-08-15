# Plan 2 — Langfuse 配置、校验、专用设置 API 与控制面 reconcile

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 设计文档 §10/§10.1/§10.2 与 §16.2——`setting/langfuse_setting` 配置结构/默认值/整组校验/不可变快照；`GET|PUT /api/option/langfuse` 专用接口（presence-aware 启用确认、secret keep/replace/clear）；通用 option 读写拒绝 `langfuse_setting.*`；`service/langfuseconfig` 事务持久化与周期 reconcile；`service/langfuse` 的 atomic binding 骨架（本计划只发布 snapshot，plan-3 扩展 runtime）；包依赖白名单测试。

**Architecture:** 注册型配置对象只服务 options 持久化兼容（relay 永不读取）；控制面 `service/langfuseconfig` 持互斥锁从 DB 完整集合重建 candidate、校验、发布；数据面 `service/langfuse` 只暴露 `LoadBinding()`。本计划不改 `updateOptionMap` 的通用底座（§10.2 明确不迁移）。

**Tech Stack:** Go, Gin, GORM, testify。

## Global Constraints

- 见 `00-master.md`。
- `service/langfuse` 直接 import 白名单（§4.1）：`common`、`constant`、`relay/common`、`relay/constant`、`relaykit/dto`、`relaykit/types`、`setting/langfuse_setting`；`service/langfuse` 禁止 import 根 `service`、`model`、`controller`、`service/langfuseconfig`、`relay/channel/**`；`relay/**` 禁止 import `service/langfuseconfig`。
- 所有数值上限/默认值必须与 §10 表逐字一致；JSON 走 `common.Marshal`。
- PUT 是整组原子操作：任一字段非法 → 400 且不持久化、不发布。

---

### Task 1: `setting/langfuse_setting` 配置结构与默认值

**Files:**
- Create: `setting/langfuse_setting/config.go`
- Test: `setting/langfuse_setting/config_test.go`

**Interfaces:**
- Produces:
  - `type LangfuseSetting struct`（§10 的 16 个字段，json tag 逐字一致）
  - `var DefaultLangfuseSetting = LangfuseSetting{...}`（默认值表）
  - `func Register()`（`config.GlobalConfig.Register("langfuse_setting", ...)`，由 `init()` 调用；注册对象仅供 options 兼容，禁止被 relay/专用 GET/runtime manager 读取）
  - `func SettingFromOptionMap(m map[string]string) LangfuseSetting`（以 `DefaultLangfuseSetting` 为基底，把 `langfuse_setting.` 前缀 key（去掉前缀后按 json tag）覆盖到副本上；解析失败保持默认）

- [ ] **Step 1: 写失败测试**：`SettingFromOptionMap` 空 map 返回默认值全集（逐字段断言 §10 默认表：enabled=false、environment="default"、sample_rate=0.1、send_content=false、max_content_bytes=65536、max_response_bytes=524288、max_in_flight_capture_bytes=536870912、max_session_body_bytes=65536、headers/paths 空、queue_size=64、batch_size=16、flush_interval_seconds=5）；部分覆盖用例（改 host/enabled/sample_rate）。
- [ ] **Step 2: 确认失败** Run: `go test ./setting/langfuse_setting -v` → FAIL（包不存在）。
- [ ] **Step 3: 实现**（照 `setting/perf_metrics_setting/config.go` 模式）：

```go
package langfuse_setting

import "github.com/QuantumNous/new-api/setting/config"

type LangfuseSetting struct {
	Enabled                 bool     `json:"enabled"`
	Host                    string   `json:"host"`
	PublicKey               string   `json:"public_key"`
	SecretKey               string   `json:"secret_key"`
	Environment             string   `json:"environment"`
	SampleRate              float64  `json:"sample_rate"`
	SendContent             bool     `json:"send_content"`
	MaxContentBytes         int      `json:"max_content_bytes"`
	MaxResponseBytes        int      `json:"max_response_bytes"`
	MaxInFlightCaptureBytes int      `json:"max_in_flight_capture_bytes"`
	MaxSessionBodyBytes     int      `json:"max_session_body_bytes"`
	SessionHeaderNames      []string `json:"session_header_names"`
	SessionBodyPaths        []string `json:"session_body_paths"`
	QueueSize               int      `json:"queue_size"`
	BatchSize               int      `json:"batch_size"`
	FlushIntervalSeconds    int      `json:"flush_interval_seconds"`
}

var DefaultLangfuseSetting = LangfuseSetting{
	Environment:             "default",
	SampleRate:              0.1,
	MaxContentBytes:         65536,
	MaxResponseBytes:        524288,
	MaxInFlightCaptureBytes: 536870912,
	MaxSessionBodyBytes:     65536,
	QueueSize:               64,
	BatchSize:               16,
	FlushIntervalSeconds:    5,
}

var registered = DefaultLangfuseSetting

func init() { config.GlobalConfig.Register("langfuse_setting", &registered) }
```

`SettingFromOptionMap`：复制默认值，遍历 `m`，`strings.TrimPrefix(k, "langfuse_setting.")` 得 json tag；用反射按 tag 匹配字段，String/Bool/Int/Float 用 `strconv` 解析，`[]string` 用 `common.Unmarshal`（string slice 在 options 表中按 §setting/config 的 `configToMap` 规则是 JSON 数组字符串）；解析失败跳过该 key。为避免 `setting/langfuse_setting` 依赖 `common` 造成不必要的耦合，直接用 `common.Unmarshal` 是允许的（`common` 属于基础包，无回环）。

- [ ] **Step 4: 通过 + Commit**

Run: `go test ./setting/langfuse_setting ./setting/... && go build ./...`

```bash
git add setting/langfuse_setting/
git commit -m "feat(langfuse): setting struct, defaults and option-map reconstruction"
```

---

### Task 2: Host 规范化、Traces URL 与整组校验

**Files:**
- Modify: `setting/langfuse_setting/config.go`（追加）
- Test: `setting/langfuse_setting/config_test.go`（追加表驱动）

**Interfaces:**
- Produces:
  - `func NormalizeHost(raw string) (scheme, authority, basePath string, err error)`
  - `func BuildTracesURL(scheme, authority, basePath string) string`
  - `func Validate(s LangfuseSetting) error`
  - `type Snapshot struct { LangfuseSetting; Scheme, Authority, BasePath, TracesURL string; Version uint64 }`（内嵌拷贝，slices 深拷贝）
  - `func BuildSnapshot(s LangfuseSetting, version uint64) (Snapshot, error)`（= Validate + NormalizeHost + 深拷贝）
  - `func (a Snapshot) EqualConfig(b Snapshot) bool`（除 Version 外全等，slice 逐元素）

- [ ] **Step 1: 写失败测试**（§14.1 settings 行的表）：

Host 表：

| 输入 | 期望 |
|---|---|
| `http://langfuse:3000` | scheme=http, authority=langfuse:3000, basePath="", TracesURL=`http://langfuse:3000/api/public/otel/v1/traces` |
| `https://x.example/langfuse/` | basePath=`/langfuse`，TracesURL=`https://x.example/langfuse/api/public/otel/v1/traces` |
| `https://x.example//a//b` | basePath=`/a/b`（path.Clean） |
| `http://[::1]:3000` | authority=`[::1]:3000` |
| `ftp://x` / `x.example`（无 scheme）/ `https://u:p@x`（userinfo）/ `https://x?q=1` / `https://x#f` / `https://x/path?` | err |
| `https://x/api/public/otel/v1/traces`（以完整 traces path 结尾） | err（歧义拒绝） |
| path 含 `\`、控制字符或空白 | err |

数值/元组表（其余字段取默认合法值，只列变化项）：

| 用例 | 期望 |
|---|---|
| 默认值配置 | 通过；reservation=640 KiB；max_queued_span_bytes=65536*2+524288=655360 ≤ 9,000,000；(64+3*16)*655360 ≤ 256 MiB |
| `max_content_bytes=4095` / `=4194305` | 拒绝（范围 4096–4194304） |
| `max_response_bytes=65535` / `=8388609` | 拒绝（65536–8388608） |
| `max_session_body_bytes=1023` / `=65537` | 拒绝（1024–65536） |
| `queue_size=15` / `=257`；`batch_size=0` / `=33`；`batch_size>queue_size` | 拒绝（16–256 / 1–32 / batch≤queue） |
| `flush_interval_seconds=0` / `=301` | 拒绝（1–300，本计划新增的保守界，写进测试注释） |
| `max_content_bytes=4194304` + response=65536, queue=16, batch=1 | 通过（单项上限可达性） |
| `max_response_bytes=8388608` + content=4096, queue=16, batch=1 | 通过（mqs=8*1024*1024+8192 ≤ 9,000,000） |
| `max_content_bytes=4194304, max_response_bytes=8388608` | 拒绝（mqs > 9,000,000） |
| `max_in_flight_capture_bytes <= reservation` | 拒绝（必须严格 ≥ reservation） |
| `sample_rate=-0.1` / `=1.1`；`environment=''` / `='Langfuse-prod'`（langfuse 前缀）/ `='A_b'`（大写）/ 41 字符 | 拒绝；`sample_rate=0` 与 `=1` 通过 |
| `enabled=true` 且 public/secret 任一为空 | 拒绝 |
| `session_header_names` 含 `Authorization`/`Cookie`（大小写不敏感）/ 非法 field name（含空格/`:`） | 拒绝 |
| `session_body_paths` 含空串或前后空白 | 拒绝 |

- [ ] **Step 2: 确认失败** Run: `go test ./setting/langfuse_setting -v` → FAIL。
- [ ] **Step 3: 实现**

```go
func NormalizeHost(raw string) (string, string, string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", "", errors.New("host 必须是绝对 http/https URL")
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" {
		return "", "", "", errors.New("host 不允许 userinfo/query/fragment/编码路径")
	}
	p := u.Path
	if !utf8.ValidString(p) || strings.ContainsAny(p, "\\\r\n\t") {
		return "", "", "", errors.New("host path 含非法字符")
	}
	basePath := path.Clean("/" + strings.Trim(p, "/"))
	if basePath == "/" {
		basePath = ""
	}
	if strings.HasSuffix(basePath, "/api/public/otel/v1/traces") {
		return "", "", "", errors.New("host 不应包含完整 traces 路径，只需填 base URL")
	}
	return u.Scheme, u.Host, basePath, nil // url.Parse 已把 scheme 小写
}

func BuildTracesURL(scheme, authority, basePath string) string {
	return (&url.URL{Scheme: scheme, Host: authority,
		Path: path.Join(basePath, "/api/public/otel/v1/traces")}).String()
}
```

`Validate` 按上面表格逐项实现；reservation/max_queued_span_bytes/tuple 用 int64 + 显式 `if a > math.MaxInt64-b` 风格的 checked 加乘（虽当前上限不会溢出，仍按 §10 要求写成 checked 形式并测试 0 值兜底）。字段名合法 HTTP header 校验：非空、仅 `!#$%&'*+-.^_`|~` 与字母数字（RFC 7230 token），再拒绝 `authorization/cookie/proxy-authorization/set-cookie`（`strings.EqualFold`）。环境校验 `regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)` + `!strings.HasPrefix(env, "langfuse")`。

`BuildSnapshot`：`Validate` → `NormalizeHost` → 拷贝结构体（slice 用 `slices.Clone`）→ 填 `Scheme/Authority/BasePath/TracesURL/Version`。`EqualConfig` 用 `reflect.DeepEqual` 比较**除 Version 外**的字段（实现：两者 Version 置 0 的副本比较，或逐字段；选一种并测试）。

- [ ] **Step 4: 通过 + Commit**

Run: `go test ./setting/langfuse_setting -v`

```bash
git add setting/langfuse_setting/
git commit -m "feat(langfuse): host normalization, traces URL and whole-group validation"
```

---

### Task 3: `service/langfuse` atomic binding 骨架 + 包依赖测试

**Files:**
- Create: `service/langfuse/runtime.go`（本任务仅 manager/binding 部分）
- Test: `service/langfuse/runtime_test.go`、`service/langfuse/package_boundaries_test.go`

**Interfaces:**
- Produces（plan-3/5b 消费的契约）:
  - `type Snapshot = langfuse_setting.Snapshot`（类型别名，数据面统一经此引用）
  - `type TelemetryRuntime struct{ ... }`（本计划为空壳占位 `struct{ version uint64 }`，plan-3 填充；**字段未定前不导出任何方法**）
  - `type RuntimeBinding struct { Snapshot Snapshot; Runtime *TelemetryRuntime }`
  - `func LoadBinding() *RuntimeBinding`（永不为 nil；初始为 disabled 默认快照）
  - `func PublishBinding(b RuntimeBinding)`（原子发布；Version 由 manager 内单调计数器在 `PublishSnapshot` 中分配——本计划提供 `func PublishSnapshot(s langfuse_setting.LangfuseSetting) error`：BuildSnapshot→PublishBinding(Runtime=nil)）
  - `func CurrentVersion() uint64`

- [ ] **Step 1: 写失败测试**：
  - 初始 `LoadBinding()` 非 nil、`Enabled=false`、`Runtime==nil`。
  - `PublishSnapshot(合法启用配置)` 后 `LoadBinding().Snapshot.Enabled==true`、`TracesURL` 正确、`Version` 单调 +1；`PublishSnapshot(非法配置)` 返回 error 且旧 binding 不变。
  - 并发 100 goroutine 交替 Load/Publish 无 race（`-race` 下跑）。
- [ ] **Step 2: 确认失败** Run: `go test ./service/langfuse -v`。
- [ ] **Step 3: 实现**：包级 `manager`（`binding atomic.Pointer[RuntimeBinding]`、`versionCounter atomic.Uint64`、启动时发布 disabled 默认 binding）。JSON/无外部依赖。
- [ ] **Step 4: 包依赖白名单测试（§4.1，一次写全）**

`package_boundaries_test.go` 两个测试：

1. **AST 直接 import 白名单**：`go/parser` 解析 `./`（包目录，相对测试文件用 `runtime.Caller` 或 `os.DirFS(".")` 取本包目录）下所有**非 `_test.go`** 文件，收集 import path；对每个以 `github.com/QuantumNous/new-api/` 开头的 import，断言在白名单 `map[string]bool`（§4.1 七个包；模块根自身不会出现）。fixture：临时构造字符串断言 `.../relaykit/dto`、`.../relaykit/types` 通过而 `.../dto`、`.../types`、`.../service`、`.../model`、`.../setting/config` 被拒（不要为了 fixture 引入真实非法 import——直接对白名单 map 做断言即可）。
2. **`go list -deps` 闭包禁用名单**：`exec.Command("go", "list", "-deps", "./service/langfuse")`（`cmd.Dir` = 仓库根，用 `runtime` 定位或 `../..`），断言输出不含 `github.com/QuantumNous/new-api/service`、`.../model`、`.../controller`、`.../service/langfuseconfig`、任何 `.../relay/channel/...`；并对 `go list -deps ./relay/...`（逐包）断言不含 `.../service/langfuseconfig`。`go` 不在 PATH 时 `t.Skip`。

- [ ] **Step 5: 通过 + Commit**

Run: `go test ./service/langfuse -race -v`

```bash
git add service/langfuse/
git commit -m "feat(langfuse): atomic runtime binding manager skeleton and package boundary tests"
```

---

### Task 4: 通用 option 读写拒绝 + 专用 GET/PUT

**Files:**
- Modify: `controller/option.go`（`isLangfuseOptionKey`、`UpdateOption` 拒绝分支、`GetOptions` 排除前缀）
- Create: `controller/option_langfuse.go`
- Modify: `router/api-router.go`（optionRoute 组内两条路由）
- Modify: `model/option.go`（新增 `AllOptionsByPrefix` 查询 helper）
- Test: `controller/option_langfuse_test.go`

**Interfaces:**
- Produces:
  - `func isLangfuseOptionKey(key string) bool`（前缀严格 `langfuse_setting.`）
  - `GET /api/option/langfuse` → `controller.GetLangfuseSetting`，响应 body（SecretKey 永不出现）：

```json
{ "success": true, "data": { "enabled": false, "host": "", "public_key": "", "secret_key_configured": false,
  "environment": "default", "sample_rate": 0.1, "send_content": false, "max_content_bytes": 65536,
  "max_response_bytes": 524288, "max_in_flight_capture_bytes": 536870912, "max_session_body_bytes": 65536,
  "session_header_names": [], "session_body_paths": [], "queue_size": 64, "batch_size": 16, "flush_interval_seconds": 5 } }
```

  - `PUT /api/option/langfuse` → `controller.UpdateLangfuseSetting`，request DTO：

```go
type langfuseUpdateRequest struct {
	Enabled                 *bool    `json:"enabled"`
	Host                    string   `json:"host"`
	PublicKey               string   `json:"public_key"`
	SecretKey               string   `json:"secret_key"`        // "" => keep 现有值
	SecretKeyClear          bool     `json:"secret_key_clear"`  // true => clear;仅最终 enabled=false 时合法
	Environment             string   `json:"environment"`
	SampleRate              *float64 `json:"sample_rate"`       // presence-aware
	SendContent             *bool    `json:"send_content"`      // presence-aware
	MaxContentBytes         int      `json:"max_content_bytes"`
	MaxResponseBytes        int      `json:"max_response_bytes"`
	MaxInFlightCaptureBytes int      `json:"max_in_flight_capture_bytes"`
	MaxSessionBodyBytes     int      `json:"max_session_body_bytes"`
	SessionHeaderNames      []string `json:"session_header_names"`
	SessionBodyPaths        []string `json:"session_body_paths"`
	QueueSize               int      `json:"queue_size"`
	BatchSize               int      `json:"batch_size"`
	FlushIntervalSeconds    int      `json:"flush_interval_seconds"`
}
```

- [ ] **Step 1: 写失败测试**（controller 层，按现有 controller 测试的 DB/中间件 fixture 方式；若 `controller` 包现有测试无 HTTP harness，则直接对 handler 函数构造 `gin.Context` + `httptest`，DB 不可用时注入内存 SQLite fixture——遵循仓库现有 controller 测试模式，实现时先看 `controller/*_test.go` 已有做法）：

用例表：
1. 通用 `PUT /api/option`（`UpdateOption`）携带 `langfuse_setting.host` → 400/错误信息含"Langfuse 专用设置接口"；`langfuse_setting.secret_key` 同样拒绝。
2. `GET /api/option/` 响应不含任何 `langfuse_setting.` key。
3. GET langfuse：DB 无记录 → 返回默认值 + `secret_key_configured=false`；DB 有 secret → `secret_key_configured=true` 且响应文本不含 secret 值。
4. PUT 成功路径：禁用→启用且 `sample_rate`+`send_content` 均出现 → 200，DB 中全部 16 个 key 落库，binding 发布（`langfuse.LoadBinding().Snapshot.Host` 更新），响应不含 secret。
5. 启用确认规则：持久化 `enabled=false`，PUT `enabled=true` 但缺 `sample_rate` 或缺 `send_content` → 400 且 DB/binding 不变；已启用状态下的普通更新（缺二者）→ 通过。
6. secret：PUT 空 secret + 已有 secret → 保留（DB 仍是旧值）；`secret_key_clear=true` 且最终 enabled=false → 清空；`secret_key_clear=true` 且 enabled=true → 400。
7. 校验失败（如 host 非法）→ 400，DB 与 binding 均不变。

- [ ] **Step 2: 确认失败** Run: `go test ./controller -run Langfuse -v`。
- [ ] **Step 3: 实现**

`model/option.go` 追加：

```go
// AllOptionsByPrefix returns all persisted option rows whose key starts with prefix.
func AllOptionsByPrefix(prefix string) (map[string]string, error) {
	var options []Option
	if err := DB.Where("`key` LIKE ?", prefix+"%").Find(&options).Error; err != nil {
		return nil, err
	}
	m := make(map[string]string, len(options))
	for _, o := range options {
		m[o.Key] = o.Value
	}
	return m, nil
}
```

（`key` 反引号引用在 PG 下不合法——PG 用 `"key"`。改用 GORM 非保留列写法不可行（key 是保留字），因此按 `common.UsingMainDatabase` 分支引用，或复用 `model/main.go` 已有的 `commonKeyCol` helper——实现时用 `commonKeyCol`，与现有代码一致。）

`controller/option.go`：`GetOptions` 的循环里加 `if strings.HasPrefix(k, "langfuse_setting.") { continue }`；`UpdateOption` 在 payment-compliance 拒绝块旁加：

```go
	if isLangfuseOptionKey(option.Key) {
		// 整组事务写入必须走专用接口,避免 Host/Key 中间状态破坏 exporter
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Langfuse 配置请使用专用设置接口 /api/option/langfuse"})
		return
	}
```

`controller/option_langfuse.go`：GET/PUT 均委托 `service/langfuseconfig`（Task 5 提供 `langfuseconfig.GetView()` / `langfuseconfig.Update(c, req)`）；错误返回沿用该文件邻近代码的响应风格。

`router/api-router.go` optionRoute 组：

```go
			optionRoute.GET("/langfuse", controller.GetLangfuseSetting)
			optionRoute.PUT("/langfuse", controller.UpdateLangfuseSetting)
```

- [ ] **Step 4: 通过 + Commit**

Run: `go test ./controller -run Langfuse -v && go build ./...`

```bash
git add controller/option.go controller/option_langfuse.go controller/option_langfuse_test.go router/api-router.go model/option.go
git commit -m "feat(langfuse): dedicated root-only settings API, generic option guard"
```

---

### Task 5: `service/langfuseconfig` 持久化/reconcile/发布 + 启动与周期 wiring

**Files:**
- Create: `service/langfuseconfig/service.go`
- Modify: `main.go`（InitResources 中 `model.InitOptionMap()` 之后同步 reconcile 一次；周期任务区加 `go langfuseconfig.StartReconcileLoop(common.SyncFrequency)`）
- Test: `service/langfuseconfig/service_test.go`

**Interfaces:**
- Consumes: `model.AllOptionsByPrefix`、`model.UpdateOptionsBulk`（事务保存，确认其内部同时更新 OptionMap——实现时读该函数，若不更新则逐 key `model.UpdateOption` 在事务外 apply，保持与现有行为一致）、`service/langfuse` 的 `PublishSnapshot`/`LoadBinding`。
- Produces:
  - `func GetView() (langfuse_view 结构, error)`（完整 DB 配置 + secret_configured，无 secret 值）
  - `func Update(req controller 传入的已解析请求) error`（互斥锁内：load→secret 合成→启用确认→Validate/BuildSnapshot→事务保存→发布；持久化失败保留旧 binding）
  - `func Reconcile() error`（互斥锁内：DB 完整集合→SettingFromOptionMap→BuildSnapshot→与 `LoadBinding().Snapshot.EqualConfig` 比较，相同 no-op，不同则 PublishSnapshot）
  - `func StartReconcileLoop(freqSec int)`（`time.Sleep` 循环调 Reconcile；错误限频 `common.SysError`）

- [ ] **Step 1: 写失败测试**：
  - Reconcile：DB 空 → 发布默认 disabled binding；DB 写入合法启用全集 → 发布且 `TracesURL` 正确；再次 Reconcile 相同配置 → Version 不变（no-op）；DB 为无效启用配置（enabled=true 缺 secret）→ 告警路径（注入 logger 或断言返回 error/保留旧 binding）。
  - Update：成功→DB 16 key + binding 发布一次；校验失败→DB/binding 不变；事务失败（注入坏 DB 或 mock 不可行时用无效配置路径）→binding 不变。
  - 并发：`Reconcile` 与 `Update` 并发执行（goroutine 各 20 次）无 race，最终 binding 与最后一次成功操作一致。
  - DB fixture：包内用 SQLite 内存库初始化 `model.DB`（照 `service` 包现有测试的 DB fixture；若 langfuseconfig 引 model 导致测试需要真实 DB，照 `model` 包测试的 setup 方式）。
- [ ] **Step 2: 确认失败**。
- [ ] **Step 3: 实现**（控制面唯一允许 import `model` 与 `service/langfuse` 的 Langfuse 包）：

Update 的启用确认逻辑（与 controller 测试用例对应）：

```go
	// persisted disabled -> enabled 时, sample_rate 与 send_content 必须显式出现
	if !persisted.Enabled && candidate.Enabled && (req.SampleRate == nil || req.SendContent == nil) {
		return errEnableConfirmationRequired
	}
```

secret 合成：`final.SecretKey = persisted.SecretKey`；`req.SecretKey != ""` → 覆盖；`req.SecretKeyClear` → 置空（且要求最终 `Enabled=false`，否则 error）。`Validate` 在合成后执行（启用时要求非空 secret）。

事务保存：把 16 个 `langfuse_setting.<json_tag>` → 值（slice 用 `common.Marshal` 成 JSON 数组字符串，与 `setting/config.configToMap` 序列化一致）交给 `model.UpdateOptionsBulk`；成功后 `PublishSnapshot(final)`。

main.go：`InitResources` 中 `model.InitOptionMap()` 调用后加：

```go
	if err := langfuseconfig.InitFromOptions(); err != nil { // 内部: 一次 Reconcile,失败仅 SysError 不阻断启动
		common.SysError("langfuse initial reconcile failed: " + err.Error())
	}
```

周期任务区（`go model.SyncOptions(common.SyncFrequency)` 旁）加 `go langfuseconfig.StartReconcileLoop(common.SyncFrequency)`。

- [ ] **Step 4: 通过 + 全量构建**

Run: `go test ./service/langfuseconfig ./service/langfuse ./controller ./setting/... && go build ./... && cd relaykit && GOWORK=off go build ./...`

- [ ] **Step 5: Commit**

```bash
git add service/langfuseconfig/ main.go
git commit -m "feat(langfuse): control-plane persistence, reconcile loop and publish wiring"
```

---

## Self-Review 已核对

- §10 全部校验规则有对应测试行；§10.1 的 secret/presence/通用拒绝在 Task 4 用例表；§10.2 的互斥锁、no-op 等值、事务→单次发布、跨实例完整集合在 Task 5；§4.1 包依赖测试在 Task 3。管理 UI 的容量提示留给 plan-7。`updateOptionMap` 通用底座未触碰（§10.2 明确）。
