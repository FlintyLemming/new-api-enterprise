# Exchange Key Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 内部应用用公司 SECRET 对用户名做 HMAC，以 `sk-<username>-<hmac>` 调用 relay / 只读 token usage / log-by-key，按该用户现有钱包或订阅扣费，不创建 `tokens` 行。

**Architecture:** `setting/exchange_key` 负责解析、HMAC、环境变量覆盖 option 后的 effective config。`TokenAuth` / `TokenAuthReadOnly` 先走现网令牌；未命中且形状匹配且功能有效时才验 HMAC、按用户名查用户，写入虚拟令牌上下文（`token_id=0`、`token_name=exchange-key`、不保存 HMAC）。`RelayInfo.AppliesTokenQuota()` 在 playground 与 exchange key 上跳过令牌额度，资金侧不变。专用 `GET/PUT /api/option/exchange-key` 永不回传 SECRET。

**Tech Stack:** Go 1.22+、Gin、GORM、`crypto/hmac`、React 19 + RHF + Zod、i18next 七语 locale。

## Global Constraints

- 默认关闭。功能有效 ≔ `effectiveEnabled && effectiveSecret != ""`。缺一则 exchange 路径不执行，现网普通令牌行为不变。
- 不在 `tokens` 表自动建 backing token。不按用户或应用做 allowlist。不自动开户。不做双 SECRET 轮换。不支持 `sk-<username>-<hmac>-<渠道ID>`。
- HMAC 消息是用户名的 UTF-8 字节，不含 `sk-`、不含后缀。SECRET 为 UTF-8 字节。比较：hex 解码成 32 字节后 `hmac.Equal`。
- 解析从右边切：去掉可选 `sk-` 后，最后一个 `-` 右侧必须恰好 64 位 hex；左侧整段为 username（1–20 个 Unicode 码点，区分大小写，允许中间 `-`，不 trim）。不是该形状 → 不当作 exchange key。
- 环境变量优先于 option：`EXCHANGE_KEY_ENABLED`（仅 `true`/`false` 视为已设置）覆盖 `exchange_key.enabled`；`EXCHANGE_KEY_SECRET` 非空则覆盖 `exchange_key.secret`。SECRET 最短 16 字符；后台写入拒绝更短。环境变量已设置但过短：视为用空 SECRET 覆盖 option（功能不生效），并打警告。
- 通用 `GET /api/option` 不返回 `exchange_key.*`。`UpdateOption` 拒绝这些 key。专用 GET 永不返回 SECRET 明文。
- `secret_from_env` 时拒绝改 `secret_key` / `secret_key_clear`。`enabled_from_env` 时若请求的 `enabled` 与 effective enabled 不同则拒绝；相同时保持已存 option 的 enabled 不变。
- 日志与上下文不保存 HMAC 后缀。`token_key` 必须为空。筛选用 `token_name=exchange-key`，不用 `token_id`。
- 封禁用户：403 + 现网 `MsgAuthUserBanned`。形状不对 / 功能关 / HMAC 错 / 用户不存在（含软删）：401 + 现网 `MsgTokenInvalid`，响应不区分原因。查用户 DB 失败：500 + `MsgDatabaseError`。
- 不改 `relaykit/`。不改 8858 Langfuse 部署。根模块 JSON 仍走 `common.*`。新 Go 测试用 `testify/require` + `testify/assert`。前端 locale 只经 `web/scripts/add-missing-keys.mjs` 再 `bun run i18n:sync`。
- 系统 option 路由与 Langfuse 一样挂在 `RootAuth`（`/api/option/*`），不要改成 `AdminAuth`。
- `setting/exchange_key` 无 Gin、无 `relaykit` 依赖。`middleware/auth.go` 不内嵌 HMAC。

---

## File structure

| File | Responsibility |
| --- | --- |
| `setting/exchange_key/parse.go` | `ParseExchangeKey`、`MACEqual`、常量 `TokenName` |
| `setting/exchange_key/config.go` | option 注册、env 覆盖、`Effective`、`BuildView`、`ApplyUpdate` |
| `setting/exchange_key/parse_test.go` | 形状与 HMAC 契约 |
| `setting/exchange_key/config_test.go` | env 覆盖、ApplyUpdate 锁定与最短 SECRET |
| `model/user.go` | `GetUserIDByUsername`（`First` + 软删语义） |
| `model/user_username_test.go` | 精确命中 / 不存在 / 软删 |
| `model/option.go` | 导入 package 以便 `handleConfigUpdate` 热更新；启动时警告短 env |
| `model/log.go` | `GetLogsByUserIDAndTokenName` |
| `controller/option_exchange_key.go` | `GET/PUT /api/option/exchange-key` |
| `controller/option_exchange_key_test.go` | view 无明文、env 锁 PUT、空 secret 保持 |
| `controller/option.go` | 通用 GET/PUT 排除 `exchange_key.` |
| `controller/log.go` | exchange 时按 user + `token_name` 查日志 |
| `controller/token.go` | exchange 时返回虚拟 usage，禁止 `GetTokenByKey` |
| `router/api-router.go` | 注册专用 option 路由 |
| `constant/context_key.go` | `ContextKeyExchangeKey` |
| `middleware/auth.go` | TokenAuth / TokenAuthReadOnly 接入 |
| `middleware/exchange_key_auth_test.go` | auth 表测 + 普通令牌回归 |
| `relay/common/relay_info.go` | `IsExchangeKey`、`AppliesTokenQuota()` |
| `relay/common/relay_info_test.go` | context → flag；quota helper |
| `service/quota.go` | PreConsume / postConsume / PreWss 跳过令牌额度 |
| `service/billing_session.go` | Settle / Refund / preConsume 回滚 / reserveToken |
| `service/exchange_key_quota_test.go` | 扣用户额度、不写 token 行、普通令牌仍改 remain |
| `web/src/features/system-settings/security/exchange-key-api.ts` | 专用 API client |
| `web/src/features/system-settings/security/exchange-key-schema.ts` | zod |
| `web/src/features/system-settings/security/exchange-key-settings-section.tsx` | 安全页一节 |
| `web/src/features/system-settings/security/section-registry.tsx` | 注册 section |
| `web/src/features/system-settings/security/__tests__/*` | env 锁定禁用输入；空 secret 不清除 |
| `web/src/i18n/locales/{en,zh,zh-TW,fr,ja,ru,vi}.json` | 经脚本写入 |
| `docker-compose.yml` | 注释掉的 `EXCHANGE_KEY_*`（不写真实 SECRET） |

不要新建 `relaykit` 文件。不要新开顶级菜单。不要把 SECRET 写进仓库、本计划或通用 option 列表。

HMAC 测试向量（SECRET = `test-secret-16ch`）：

| username | hmac hex |
| --- | --- |
| `alice` | `4a5727f69871ef400b44edf149bcfc6c9145155726e2f1653617e74ac5c5e655` |
| `foo-bar` | `998068798a8831c9f16243767b026e2ec3b64245b4adca698c9622622905106f` |
| `foo` | `b89daffb76f91ea398aa13cf14938cda411c0f4d653984b3d1cbc8d057d9406f` |

---

### Task 1: Parse, HMAC, effective config

**Files:**
- Create: `setting/exchange_key/parse.go`
- Create: `setting/exchange_key/config.go`
- Create: `setting/exchange_key/parse_test.go`
- Create: `setting/exchange_key/config_test.go`
- Modify: `model/option.go`（import `setting/exchange_key`；`InitOptionMap` 在 `loadOptionsFromDatabase()` 之后调用 `exchange_key.LogMisconfiguredEnv()`）

**Interfaces:**
- Consumes: `setting/config.GlobalConfig.Register`；`os.LookupEnv`；`common.SysError`。
- Produces:
  - `const OptionKeyPrefix = "exchange_key."`
  - `const TokenName = "exchange-key"`
  - `const MinSecretLength = 16`
  - `const EnvEnabled = "EXCHANGE_KEY_ENABLED"`
  - `const EnvSecret = "EXCHANGE_KEY_SECRET"`
  - `type Setting struct { Enabled bool `json:"enabled"`; Secret string `json:"secret"` }`
  - `func ParseExchangeKey(raw string) (username string, mac []byte, ok bool)`
  - `func MACEqual(secret, username string, mac []byte) bool`
  - `type EffectiveConfig struct { Enabled, SecretConfigured, EnabledFromEnv, SecretFromEnv bool; Secret string }`
  - `func Effective() EffectiveConfig` — `Enabled` 已是 `effectiveEnabled && effectiveSecret != ""` 时仍分别返回开关与 secret；`IsEffective()` 才是两者同时成立。
  - `func IsEffective() bool`
  - `func BuildView() SettingView`
  - `type SettingView struct { Enabled bool `json:"enabled"`; SecretConfigured bool `json:"secret_configured"`; SecretFromEnv bool `json:"secret_from_env"`; EnabledFromEnv bool `json:"enabled_from_env"` }`
  - `type UpdateRequest struct { Enabled *bool `json:"enabled"`; SecretKey string `json:"secret_key"`; SecretKeyClear bool `json:"secret_key_clear"` }`
  - `func ApplyUpdate(stored Setting, req UpdateRequest) (Setting, error)`
  - `func GetStored() Setting`
  - `func ReplaceStoredForTest(t *testing.T, s Setting)`
  - `func LogMisconfiguredEnv()`
  - 错误值：`ErrSecretTooShort`、`ErrSecretRequired`、`ErrSecretClearWhileEnabled`、`ErrSecretLockedByEnv`、`ErrEnabledLockedByEnv`

`Effective()` 规则：

1. stored = 注册的 `Setting`（option / `handleConfigUpdate` 写入）。
2. `EXCHANGE_KEY_ENABLED`：未设置或空 → 用 stored.Enabled；值为 `true`/`false`（只认这两个小写字面量）→ 覆盖且 `EnabledFromEnv=true`；其他值 → `common.SysError` 警告并视为未设置。
3. `EXCHANGE_KEY_SECRET`：未设置或空 → 用 stored.Secret；非空且 `len >= 16` → 覆盖且 `SecretFromEnv=true`；非空且 `len < 16` → `Secret=""`、`SecretFromEnv=true`、`SecretConfigured=false`，并 `SysError` 警告（用上次已警告的原文去重，避免每个请求刷屏）。
4. stored.Secret 若 `< 16` 也视为空。
5. `BuildView().Enabled` 是 **stored/env 覆盖后的开关**（即使 secret 为空也如实显示开关）。`SecretConfigured` 是 effective secret 非空。`IsEffective()` = 该开关为 true **且** effective secret 非空。

`ApplyUpdate`：先读 env overlay（与 `Effective` 同一套 `readEnv`）。`enabled_from_env` 且 `req.Enabled != nil` 且 `*req.Enabled !=` overlay 后的 enabled → `ErrEnabledLockedByEnv`；若相等则 **不要改** stored.Enabled。`secret_from_env`（env 非空，含过短）且 (`req.SecretKey != ""` 或 `req.SecretKeyClear`) → `ErrSecretLockedByEnv`。新 secret 长度 `< 16` → `ErrSecretTooShort`。`secret_key_clear` 仅当 **candidate.Enabled 为 false** 时允许。最终若 overlay 后的 effective enabled 为 true 且 effective secret 为空 → `ErrSecretRequired`。返回的是要 **写入 option** 的 candidate（env 覆盖的字段保持 stored 原值）。

- [ ] **Step 1: Write the failing parse/HMAC tests**

Create `setting/exchange_key/parse_test.go`:

```go
package exchange_key

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "test-secret-16ch"

func TestParseExchangeKeyAcceptsUsernameAndHexSuffix(t *testing.T) {
	mac, err := hex.DecodeString("4a5727f69871ef400b44edf149bcfc6c9145155726e2f1653617e74ac5c5e655")
	require.NoError(t, err)

	username, got, ok := ParseExchangeKey("sk-alice-4a5727f69871ef400b44edf149bcfc6c9145155726e2f1653617e74ac5c5e655")
	require.True(t, ok)
	assert.Equal(t, "alice", username)
	assert.Equal(t, mac, got)

	username, got, ok = ParseExchangeKey("alice-4a5727f69871ef400b44edf149bcfc6c9145155726e2f1653617e74ac5c5e655")
	require.True(t, ok)
	assert.Equal(t, "alice", username)
	assert.Equal(t, mac, got)
}

func TestParseExchangeKeyKeepsHyphenatedUsername(t *testing.T) {
	username, mac, ok := ParseExchangeKey("sk-foo-bar-998068798a8831c9f16243767b026e2ec3b64245b4adca698c9622622905106f")
	require.True(t, ok)
	assert.Equal(t, "foo-bar", username)
	assert.Len(t, mac, 32)
}

func TestParseExchangeKeyRejectsNonExchangeShapes(t *testing.T) {
	cases := []string{
		"sk-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"sk-alice-deadbeef",
		"sk-alice-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
		"alice",
		"sk-alice",
		"",
		"sk-" + strings.Repeat("a", 21) + "-4a5727f69871ef400b44edf149bcfc6c9145155726e2f1653617e74ac5c5e655",
		"sk--4a5727f69871ef400b44edf149bcfc6c9145155726e2f1653617e74ac5c5e655",
		"sk-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-12",
	}
	for _, raw := range cases {
		_, _, ok := ParseExchangeKey(raw)
		assert.False(t, ok, raw)
	}
}

func TestParseExchangeKeyDoesNotTrimUsername(t *testing.T) {
	_, _, ok := ParseExchangeKey("sk- alice-4a5727f69871ef400b44edf149bcfc6c9145155726e2f1653617e74ac5c5e655")
	assert.True(t, ok)
	username, _, ok := ParseExchangeKey("sk- alice-4a5727f69871ef400b44edf149bcfc6c9145155726e2f1653617e74ac5c5e655")
	require.True(t, ok)
	assert.Equal(t, " alice", username)
}

func TestMACEqualAcceptsMixedCaseHexAndRejectsWrongInputs(t *testing.T) {
	lower := "4a5727f69871ef400b44edf149bcfc6c9145155726e2f1653617e74ac5c5e655"
	mac, err := hex.DecodeString(strings.ToUpper(lower))
	require.NoError(t, err)
	assert.True(t, MACEqual(testSecret, "alice", mac))
	assert.False(t, MACEqual(testSecret, "bob", mac))
	assert.False(t, MACEqual("other-secret-16ch", "alice", mac))
}
```

Create `setting/exchange_key/config_test.go`（先写这些，实现前会失败）：

```go
package exchange_key

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEffectiveEnvOverridesOption(t *testing.T) {
	ReplaceStoredForTest(t, Setting{Enabled: false, Secret: "option-secret-16"})
	t.Setenv(EnvEnabled, "true")
	t.Setenv(EnvSecret, testSecret)

	got := Effective()
	assert.True(t, got.Enabled)
	assert.Equal(t, testSecret, got.Secret)
	assert.True(t, got.EnabledFromEnv)
	assert.True(t, got.SecretFromEnv)
	assert.True(t, got.SecretConfigured)
	assert.True(t, IsEffective())
}

func TestEffectiveShortEnvSecretDisablesFeature(t *testing.T) {
	ReplaceStoredForTest(t, Setting{Enabled: true, Secret: "option-secret-16"})
	t.Setenv(EnvSecret, "too-short")
	t.Setenv(EnvEnabled, "true")

	got := Effective()
	assert.True(t, got.Enabled)
	assert.Equal(t, "", got.Secret)
	assert.True(t, got.SecretFromEnv)
	assert.False(t, got.SecretConfigured)
	assert.False(t, IsEffective())
}

func TestBuildViewNeverIncludesSecret(t *testing.T) {
	ReplaceStoredForTest(t, Setting{Enabled: true, Secret: testSecret})
	t.Setenv(EnvEnabled, "")
	t.Setenv(EnvSecret, "")

	view := BuildView()
	assert.True(t, view.Enabled)
	assert.True(t, view.SecretConfigured)
	assert.False(t, view.SecretFromEnv)
	assert.False(t, view.EnabledFromEnv)
}

func TestApplyUpdateEmptySecretKeepsStored(t *testing.T) {
	stored := Setting{Enabled: true, Secret: testSecret}
	enabled := true
	got, err := ApplyUpdate(stored, UpdateRequest{Enabled: &enabled, SecretKey: ""})
	require.NoError(t, err)
	assert.Equal(t, testSecret, got.Secret)
	assert.True(t, got.Enabled)
}

func TestApplyUpdateRejectsShortSecretAndEnvLocks(t *testing.T) {
	t.Setenv(EnvSecret, testSecret)
	t.Setenv(EnvEnabled, "true")
	enabled := false
	_, err := ApplyUpdate(Setting{Enabled: true, Secret: ""}, UpdateRequest{Enabled: &enabled})
	require.ErrorIs(t, err, ErrEnabledLockedByEnv)

	_, err = ApplyUpdate(Setting{Enabled: true, Secret: ""}, UpdateRequest{SecretKey: "another-secret-16"})
	require.ErrorIs(t, err, ErrSecretLockedByEnv)
}

func TestApplyUpdateClearRequiresDisabled(t *testing.T) {
	t.Setenv(EnvEnabled, "")
	t.Setenv(EnvSecret, "")
	enabled := true
	_, err := ApplyUpdate(Setting{Enabled: true, Secret: testSecret}, UpdateRequest{
		Enabled: &enabled, SecretKeyClear: true,
	})
	require.ErrorIs(t, err, ErrSecretClearWhileEnabled)

	enabled = false
	got, err := ApplyUpdate(Setting{Enabled: true, Secret: testSecret}, UpdateRequest{
		Enabled: &enabled, SecretKeyClear: true,
	})
	require.NoError(t, err)
	assert.False(t, got.Enabled)
	assert.Equal(t, "", got.Secret)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `GOWORK=off go test ./setting/exchange_key/ -count=1`

Expected: FAIL，`exchange_key` package 不存在 / `ParseExchangeKey` undefined。

- [ ] **Step 3: Write the implementation**

Create `setting/exchange_key/parse.go`:

```go
package exchange_key

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"
)

const (
	TokenName       = "exchange-key"
	hmacHexLength   = 64
	hmacByteLength  = 32
	maxUsernameRunes = 20
)

func ParseExchangeKey(raw string) (username string, mac []byte, ok bool) {
	trimmed := strings.TrimPrefix(raw, "sk-")
	idx := strings.LastIndex(trimmed, "-")
	if idx <= 0 {
		return "", nil, false
	}
	username = trimmed[:idx]
	suffix := trimmed[idx+1:]
	if utf8.RuneCountInString(username) < 1 || utf8.RuneCountInString(username) > maxUsernameRunes {
		return "", nil, false
	}
	if len(suffix) != hmacHexLength {
		return "", nil, false
	}
	mac, err := hex.DecodeString(suffix)
	if err != nil || len(mac) != hmacByteLength {
		return "", nil, false
	}
	return username, mac, true
}

func MACEqual(secret, username string, mac []byte) bool {
	if secret == "" || username == "" || len(mac) != hmacByteLength {
		return false
	}
	expected := hmac.New(sha256.New, []byte(secret))
	_, _ = expected.Write([]byte(username))
	return hmac.Equal(expected.Sum(nil), mac)
}
```

Create `setting/exchange_key/config.go`（完整实现上面的 Interfaces：`Register("exchange_key", stored)`，`readEnv`，`Effective`，`IsEffective`，`BuildView`，`ApplyUpdate`，`GetStored`，`ReplaceStoredForTest`，`LogMisconfiguredEnv`）。`stored` 是 package 级 `Setting` 指针，默认 `Enabled: false, Secret: ""`。

`ReplaceStoredForTest` 必须保存旧值并 `t.Cleanup` 还原。这些测试不要 `t.Parallel()`。

`LogMisconfiguredEnv`：若 env secret 非空且过短，或 enabled 字面量非法，调用 `common.SysError`。`Effective()` 在同样条件下也警告，用 package 级 `lastWarned` 字符串去重。

ApplyUpdate 错误字符串（`errors.New`）：

- `SECRET 长度不能少于 16 个字符`
- `启用 Exchange Key 时必须配置 SECRET`
- `只有在关闭 Exchange Key 后才能清除 SECRET`
- `SECRET 已由环境变量 EXCHANGE_KEY_SECRET 配置，无法在后台修改`
- `开关已由环境变量 EXCHANGE_KEY_ENABLED 配置，无法在后台修改`

Modify `model/option.go`：增加 import，并在 `InitOptionMap` 的 `loadOptionsFromDatabase()` 之后调用 `exchange_key.LogMisconfiguredEnv()`。

- [ ] **Step 4: Run tests to verify they pass**

Run: `GOWORK=off go test ./setting/exchange_key/ -count=1`

Expected: PASS。再跑 `GOWORK=off go test ./model/ -count=1 -timeout 60s` 确认 option import 不破坏现有测试。

- [ ] **Step 5: Commit**

```bash
git add setting/exchange_key/parse.go setting/exchange_key/config.go \
  setting/exchange_key/parse_test.go setting/exchange_key/config_test.go model/option.go
git commit -m "$(cat <<'EOF'
feat: add exchange-key HMAC parse and env-over-option config

EOF
)"
```

---

### Task 2: Dedicated option API, generic exclusion, username lookup

**Files:**
- Create: `controller/option_exchange_key.go`
- Create: `controller/option_exchange_key_test.go`
- Create: `model/user_username_test.go`
- Modify: `model/user.go`（在 `GetUserById` 旁增加 `GetUserIDByUsername`）
- Modify: `controller/option.go`（`isExchangeKeyOptionKey`，GET 跳过，PUT 拒绝）
- Modify: `router/api-router.go`（与 langfuse 路由并列）
- Modify: `docker-compose.yml`（只加注释掉的 env，不写真实 SECRET）

**Interfaces:**
- Consumes: Task 1 的 `ApplyUpdate`、`BuildView`、`GetStored`、`OptionKeyPrefix`、`ReplaceStoredForTest`。
- Produces:
  - `func GetUserIDByUsername(username string) (int, error)` — `DB.Select("id").Where("username = ?", username).First`；空用户名直接 `gorm.ErrRecordNotFound`；软删行不会被 `First` 命中。
  - `GET /api/option/exchange-key` → `{success, message, data: SettingView}`
  - `PUT /api/option/exchange-key` body = `UpdateRequest`；成功后 `model.UpdateOptionsBulk` 写入 `exchange_key.enabled` / `exchange_key.secret`；`recordManageAudit(c, "option.update", map[string]interface{}{"key": "exchange_key"})`（不记 secret）。
  - 通用 option：`isExchangeKeyOptionKey` = `strings.HasPrefix(key, exchange_key.OptionKeyPrefix)`，与 langfuse 同样过滤/拒绝，拒绝文案：`Exchange Key 配置请使用专用设置接口 /api/option/exchange-key`。

- [ ] **Step 1: Write the failing tests**

`model/user_username_test.go`：sqlite `:memory:` AutoMigrate `User`，`common.RedisEnabled=false`，cleanup 还原 `model.DB`。用例：

1. 创建 `Username: "Alice"`，`GetUserIDByUsername("Alice")` 等于其 Id；`GetUserIDByUsername("alice")` 为 `ErrRecordNotFound`（SQLite 默认大小写敏感）。
2. 不存在的名字 → `ErrRecordNotFound`。
3. `DB.Delete` 软删后 `GetUserIDByUsername` → `ErrRecordNotFound`。

`controller/option_exchange_key_test.go`：照 `controller/option_langfuse_test.go` 的 `useLangfuseOptionDB` 建 sqlite Option 表（不必迁 Langfuse binding）。辅助函数直接调 `GetExchangeKeySetting` / `UpdateExchangeKeySetting`。

表测：

1. 默认 GET：`enabled=false`，`secret_configured=false`，body 不含 `test-secret` 也不含 `"secret":`。
2. PUT `enabled=true, secret_key=test-secret-16ch` → 200；再 GET `secret_configured=true` 且 body 不含该 secret。
3. PUT `secret_key=""` 保持已存；option 行 `exchange_key.secret` 仍是原值。
4. `t.Setenv(EnvSecret, testSecret)` 后 PUT 新 secret → 400 且 `ErrSecretLockedByEnv` 文案。
5. `GetOptions` 的 JSON 不含 `exchange_key.`。
6. `UpdateOption` key=`exchange_key.enabled` → 400 专用接口文案。

- [ ] **Step 2: Run tests to verify they fail**

Run: `GOWORK=off go test ./model/ -run TestGetUserIDByUsername -count=1`

Expected: FAIL，`GetUserIDByUsername` undefined。

Run: `GOWORK=off go test ./controller/ -run 'Test(Get|Update)ExchangeKey|TestGetOptionsExcludesExchangeKey' -count=1`

Expected: FAIL，handler / 函数不存在。

- [ ] **Step 3: Write the implementation**

`model/user.go`：

```go
func GetUserIDByUsername(username string) (int, error) {
	if username == "" {
		return 0, gorm.ErrRecordNotFound
	}
	var user User
	err := DB.Select("id").Where("username = ?", username).First(&user).Error
	if err != nil {
		return 0, err
	}
	return user.Id, nil
}
```

`controller/option_exchange_key.go` 照 `option_langfuse.go`：GET 调 `exchange_key.BuildView()`；PUT 用 `common.DecodeJson` 解 `UpdateRequest`，`ApplyUpdate(GetStored(), req)`，校验错误 400，`model.UpdateOptionsBulk` 失败走 `common.ApiError`。Bulk map：

```go
map[string]string{
  exchange_key.OptionKeyPrefix + "enabled": strconv.FormatBool(candidate.Enabled),
  exchange_key.OptionKeyPrefix + "secret":  candidate.Secret,
}
```

`controller/option.go`：增加 `isExchangeKeyOptionKey`，在 langfuse 判断旁边同样 skip / reject。

`router/api-router.go` 在 langfuse 路由下：

```go
optionRoute.GET("/exchange-key", controller.GetExchangeKeySetting)
optionRoute.PUT("/exchange-key", controller.UpdateExchangeKeySetting)
```

`docker-compose.yml` 的 `environment:` 里只加注释（不要填真实 secret）：

```yaml
#      - EXCHANGE_KEY_ENABLED=false  # HMAC username keys; default off. Production SECRET must not be committed.
#      - EXCHANGE_KEY_SECRET=        # min 16 chars; non-empty overrides the admin option
```

- [ ] **Step 4: Run tests to verify they pass**

Run:

```bash
GOWORK=off go test ./model/ -run TestGetUserIDByUsername -count=1
GOWORK=off go test ./controller/ -run 'ExchangeKey|TestGetOptionsHidesLangfuse' -count=1
```

Expected: PASS。现有 `TestGetOptionsHidesLangfuseSettingKeys` 仍过；新 GET 排除测试也过。

- [ ] **Step 5: Commit**

```bash
git add model/user.go model/user_username_test.go \
  controller/option_exchange_key.go controller/option_exchange_key_test.go \
  controller/option.go router/api-router.go docker-compose.yml
git commit -m "$(cat <<'EOF'
feat: add dedicated exchange-key option API without exposing the secret

EOF
)"
```

---

### Task 3: TokenAuth virtual context, RelayInfo flag, read-only usage/logs

**Files:**
- Modify: `constant/context_key.go`
- Modify: `middleware/auth.go`
- Create: `middleware/exchange_key_auth_test.go`
- Modify: `relay/common/relay_info.go`（`IsExchangeKey` 字段；`genBaseRelayInfo` 从 context 填入；`ToString` 打出该 flag；`AppliesTokenQuota()`）
- Modify: `relay/common/relay_info_test.go`
- Modify: `model/log.go`（`GetLogsByUserIDAndTokenName`）
- Modify: `controller/log.go`（`GetLogByKey`）
- Modify: `controller/token.go`（`GetTokenUsage`）

**Interfaces:**
- Consumes: `exchange_key.IsEffective`、`ParseExchangeKey`、`MACEqual`、`Effective().Secret`、`TokenName`、`GetUserIDByUsername`、`GetUserCache`。
- Produces:
  - `constant.ContextKeyExchangeKey ContextKey = "exchange_key"`
  - `RelayInfo.IsExchangeKey bool`
  - `func (info *RelayInfo) AppliesTokenQuota() bool` = `info != nil && !info.IsPlayground && !info.IsExchangeKey`
  - 虚拟上下文（**不要**调用 `SetupContextForToken` 的渠道后缀逻辑）：

| 字段 | 值 |
| --- | --- |
| `id` | 该用户 |
| `token_id` | `0` |
| `token_name` | `exchange-key` |
| `token_key` | `""` |
| `token_unlimited_quota` | `true` |
| token group / model limit / IP allowlist / auto groups | 不设 / 关 |
| `ContextKeyUsingGroup` | 用户默认分组 |
| `ContextKeyExchangeKey` | `true` |
| `userCache.WriteContext` | 与现网 TokenAuth 相同 |

  - TokenAuth 顺序：现有 Bearer / WS / Anthropic / Gemini / MJ 归一化 → 保留 **归一化后、切第一段之前** 的 `rawKey` → `ValidateUserToken(第一段)` → 命中（`token != nil`）完全走现网（含管理员 `parts[1]`）**不要再验 HMAC** → `token == nil` 且非 `ErrDatabase` 时才 `setupExchangeKey`。
  - HMAC 失败或用户不存在：返回 false，由现网 401 `MsgTokenInvalid` 收口（不要先查用户）。
  - 封禁：`setupExchangeKey` 内 403 `MsgAuthUserBanned` 后 abort。
  - DB 错误：500 `MsgDatabaseError`。
  - `TokenAuthReadOnly`：保持现有 Authorization 读取（不要新加 Anthropic/Gemini/WS 归一化）。`GetTokenByKey` 在 `ErrRecordNotFound` 时走同一 `setupExchangeKey`；成功后 ReadOnly 也要写 `token_name` / `token_id=0` / 空 `token_key` / `ContextKeyExchangeKey`，以便 usage/log。ReadOnly 的 401/403/500 **保持现有 JSON `{success,message}`**，不要改成 OpenAI error envelope。把写响应抽成参数，避免 TokenAuth 与 ReadOnly 混用 envelope。
  - `GetTokenUsage`：若 `GetContextKeyBool(ContextKeyExchangeKey)`，返回 `name=exchange-key`、`unlimited_quota=true`、granted/used/available=0、`model_limits_enabled=false`、`expires_at=0`，**禁止** `GetTokenByKey`。
  - `GetLogByKey`：exchange 时用 `user_id` + `token_name=exchange-key`；`token_id==0` 的非 exchange 请求仍返回「无效的令牌」。

推荐把共享逻辑收成：

```go
func setupExchangeKey(c *gin.Context, rawKey string, writeBanned, writeDBError func()) bool
```

HMAC 在 `exchange_key` 包内完成；`auth.go` 只调用 `ParseExchangeKey` / `MACEqual` / `IsEffective`。

- [ ] **Step 1: Write the failing tests**

`relay/common/relay_info_test.go` 追加：

```go
func TestAppliesTokenQuota(t *testing.T) {
	assert.False(t, (*RelayInfo)(nil).AppliesTokenQuota())
	assert.True(t, (&RelayInfo{}).AppliesTokenQuota())
	assert.False(t, (&RelayInfo{IsPlayground: true}).AppliesTokenQuota())
	assert.False(t, (&RelayInfo{IsExchangeKey: true}).AppliesTokenQuota())
}

func TestGenRelayInfoCopiesExchangeKeyFlag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	commonhost.SetContextKey(ctx, constant.ContextKeyExchangeKey, true)

	info, err := GenRelayInfo(ctx, types.RelayFormatOpenAI, nil, nil)
	require.NoError(t, err)
	assert.True(t, info.IsExchangeKey)
}
```

（`commonhost` 若与 `package common` 冲突，直接 `github.com/QuantumNous/new-api/common`.SetContextKey。`GenRelayInfo` 已在同包。）

`middleware/exchange_key_auth_test.go`：sqlite AutoMigrate `User`+`Token`，Redis off。`ReplaceStoredForTest` + `t.Setenv` 打开功能。助手 `exchangeKey(username)` 拼 `sk-{user}-{hex}`。

用 `gin.New()` + `TokenAuth()` 挂 `POST /v1/chat/completions`，成功 handler 把 context 写成 JSON。

表：

1. 有效钥匙 → 200，`id` 对，`token_id==0`，`token_name==exchange-key`，`token_key==""`，`exchange==true`，`group` 为用户分组。body 不含 hmac hex。
2. 功能关（stored enabled=false，无 env）→ 401。
3. 错 MAC → 401。
4. 未知用户（正确 MAC，用户不存在）→ 401，**body 与错 MAC 完全相同**。
5. 封禁用户 → 403，body 含 `auth.user_banned` 或翻译后的 `MsgAuthUserBanned`（测试里 `TranslateMessage` 默认返回 key）。
6. 普通 48 位 key 令牌仍 200 且 `token_id` 为真实 id、`exchange==false`。
7. 管理员用户的令牌 `sk-<48位>-<渠道ID>` 仍设置 `specific_channel_id`（handler 读 `c.GetString("specific_channel_id")`）。
8. `TokenAuthReadOnly` 有效钥匙 → 200，`token_key` 空。

渠道 ID 用例：用户 `Role: common.RoleAdminUser`，token.Key 为 48 个 `a`，Authorization `Bearer sk-`+48a+`-42`。

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
GOWORK=off go test ./relay/common/ -run 'AppliesTokenQuota|ExchangeKeyFlag' -count=1
GOWORK=off go test ./middleware/ -run ExchangeKey -count=1
```

Expected: FAIL（方法/字段/中间件行为不存在）。

- [ ] **Step 3: Write the implementation**

`constant/context_key.go` 在 user keys 后增加 `ContextKeyExchangeKey`。

`relay/common/relay_info.go`：`IsPlayground` 旁加 `IsExchangeKey`；`genBaseRelayInfo` 在 playground 判断后：

```go
info.IsExchangeKey = common.GetContextKeyBool(c, constant.ContextKeyExchangeKey)
```

```go
func (info *RelayInfo) AppliesTokenQuota() bool {
	return info != nil && !info.IsPlayground && !info.IsExchangeKey
}
```

`middleware/auth.go`：TokenAuth 在 TrimPrefix/`Split` 之前保存 `rawKey`。`ValidateUserToken` 失败且 `errors.Is(err, model.ErrDatabase)` 仍 500；否则若 `token == nil` 且 `setupExchangeKey(...)` 则 `c.Next(); return`；否则现网 401。

`setupExchangeKey` 伪代码：

```
if !exchange_key.IsEffective() { return false }
username, mac, ok := exchange_key.ParseExchangeKey(rawKey)
if !ok { return false }
if !exchange_key.MACEqual(exchange_key.Effective().Secret, username, mac) { return false }
userID, err := model.GetUserIDByUsername(username)
if errors.Is(err, gorm.ErrRecordNotFound) { return false }
if err != nil { writeDBError(); return true }
cache, err := model.GetUserCache(userID)
if err != nil { writeDBError(); return true }
if cache.Status != common.UserStatusEnabled { writeBanned(); return true }
c.Set("id", cache.Id)
c.Set("token_id", 0)
c.Set("token_name", exchange_key.TokenName)
c.Set("token_key", "")
c.Set("token_unlimited_quota", true)
c.Set("token_model_limit_enabled", false)
common.SetContextKey(c, constant.ContextKeyUsingGroup, cache.Group)
common.SetContextKey(c, constant.ContextKeyExchangeKey, true)
cache.WriteContext(c)
return true
```

TokenAuth 的 banned/db 用 `abortWithOpenAiMessage`。ReadOnly 用现有 `c.JSON` + `c.Abort`。

`GetTokenUsage` 在解析 Bearer 之后、`GetTokenByKey` 之前读 context flag（middleware 已跑过）。`GetLogByKey` 同样。

`GetLogsByUserIDAndTokenName` 复制 `GetLogByTokenId` 的 order / ClickHouse / `MaxRecentItems` / `formatUserLogs`，加上 `user_id = ? AND token_name = ?`。

- [ ] **Step 4: Run tests to verify they pass**

```bash
GOWORK=off go test ./relay/common/ -run 'AppliesTokenQuota|ExchangeKeyFlag' -count=1
GOWORK=off go test ./middleware/ -count=1
GOWORK=off go test ./controller/ -run 'ExchangeKey|Langfuse' -count=1
```

Expected: PASS。现有 `middleware/auth_test.go` 仪表盘用例仍过。

- [ ] **Step 5: Commit**

```bash
git add constant/context_key.go middleware/auth.go middleware/exchange_key_auth_test.go \
  relay/common/relay_info.go relay/common/relay_info_test.go \
  model/log.go controller/log.go controller/token.go
git commit -m "$(cat <<'EOF'
feat: authenticate HMAC exchange keys without storing the MAC

EOF
)"
```

---

### Task 4: Skip token quota, keep user funding

**Files:**
- Modify: `service/quota.go`（`PreConsumeTokenQuota`、`postConsumeQuotaWithResult`、`PreWssConsumeQuota`）
- Modify: `service/billing_session.go`（`Settle`、`Refund`、`preConsume` 回滚、`reserveToken`）
- Create: `service/exchange_key_quota_test.go`

**Interfaces:**
- Consumes: `RelayInfo.AppliesTokenQuota()`（Task 3）。
- Produces: 所有令牌预扣 / 增减 / 回滚改为 `AppliesTokenQuota()`；资金侧（`DecreaseUserQuota` / 订阅）不改。`shouldTrust` 不改（exchange 设了 `token_unlimited_quota=true`，钱包信任旁路保持现网 Unlimited 语义；订阅仍禁止信任旁路）。

替换点必须全部改掉，不要漏 `Refund` 闭包里拷贝的 `isPlayground`：

```go
appliesTokenQuota := s.relayInfo.AppliesTokenQuota()
```

`PreWssConsumeQuota`：若 `!relayInfo.AppliesTokenQuota()`，**不要** `GetTokenByKey`，令牌额度视为充足，仍检查用户额度并 `PostConsumeQuota`。

- [ ] **Step 1: Write the failing tests**

`service/exchange_key_quota_test.go`：sqlite AutoMigrate `User`+`Token`，`RedisEnabled=false`，`BatchUpdateEnabled=false`。

```go
func TestExchangeKeyPostConsumeDoesNotTouchTokens(t *testing.T) {
	// 用户 quota=1000，不创建任何 token 行
	info := &relaycommon.RelayInfo{
		UserId: user.Id, TokenId: 0, TokenKey: "", TokenUnlimited: true, IsExchangeKey: true,
	}
	require.NoError(t, PostConsumeQuota(info, 10, 0, false))

	var tokenCount int64
	require.NoError(t, model.DB.Model(&model.Token{}).Count(&tokenCount).Error)
	assert.Equal(t, int64(0), tokenCount)

	fresh, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	assert.Equal(t, 990, fresh)
}

func TestLimitedTokenStillDecreasesRemainQuota(t *testing.T) {
	info := &relaycommon.RelayInfo{
		UserId: user.Id, TokenId: token.Id, TokenKey: token.Key, TokenUnlimited: false,
	}
	require.NoError(t, PreConsumeTokenQuota(info, 5))
	var got model.Token
	require.NoError(t, model.DB.First(&got, token.Id).Error)
	assert.Equal(t, token.RemainQuota-5, got.RemainQuota)
}

func TestPreWssConsumeQuotaExchangeKeySkipsMissingTokenRow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{
		UserId: user.Id, TokenId: 0, TokenKey: "", TokenUnlimited: true,
		IsExchangeKey: true, OriginModelName: "gpt-4", UsingGroup: "default", UserGroup: "default",
	}
	err := PreWssConsumeQuota(c, info, &dto.RealtimeUsage{
		InputTokenDetails:  dto.InputTokenDetails{TextTokens: 1},
		OutputTokenDetails: dto.OutputTokenDetails{TextTokens: 1},
	})
	require.NoError(t, err)
}
```

再加 `NewBillingSession` + `wallet_only`：用户 quota 足够、`IsExchangeKey=true`、`ForcePreConsume=true`（避开信任旁路），`preConsumedQuota=8` 后用户额度减少且 token 表仍 0 行。需要 `UserSetting.BillingPreference = "wallet_only"`。

- [ ] **Step 2: Run tests to verify they fail**

Run: `GOWORK=off go test ./service/ -run 'ExchangeKey|LimitedTokenStill|PreWssConsumeQuotaExchangeKey' -count=1`

Expected: FAIL（`PreWssConsumeQuota` 因空 key / `GetTokenByKey` 失败；或 `PostConsumeQuota` 去更新 `id=0`）。

- [ ] **Step 3: Write the implementation**

`PreConsumeTokenQuota`：

```go
if !relayInfo.AppliesTokenQuota() {
	return nil
}
```

`postConsumeQuotaWithResult` 令牌分支：`if relayInfo.AppliesTokenQuota() { ... }`。

`BillingSession` 四处 `IsPlayground` 令牌判断改为 `AppliesTokenQuota()`。`Refund` 闭包捕获 `appliesTokenQuota` 而不是只拷 `isPlayground`。

`PreWssConsumeQuota`：

```go
var tokenRemainOK = true
if relayInfo.AppliesTokenQuota() {
	token, err := model.GetTokenByKey(strings.TrimPrefix(relayInfo.TokenKey, "sk-"), false)
	if err != nil {
		return err
	}
	tokenRemainOK = token.UnlimitedQuota || token.RemainQuota >= quota
	// 把原来的 token 不足检查移到这里
}
```

保持用户额度检查不变。

- [ ] **Step 4: Run tests to verify they pass**

```bash
GOWORK=off go test ./service/ -run 'ExchangeKey|LimitedTokenStill|PreWssConsumeQuotaExchangeKey|BillingSessionReserve|AttachQuotaSaturation' -count=1
GOWORK=off go test ./middleware/ -run ExchangeKey -count=1
```

Expected: PASS。现有 playground 结算测试仍过（`AppliesTokenQuota()` 对 playground 仍为 false）。

- [ ] **Step 5: Commit**

```bash
git add service/quota.go service/billing_session.go service/exchange_key_quota_test.go
git commit -m "$(cat <<'EOF'
fix: skip token-row quota for exchange keys while billing the user

EOF
)"
```

---

### Task 5: Security settings UI and i18n

**Files:**
- Create: `web/src/features/system-settings/security/exchange-key-api.ts`
- Create: `web/src/features/system-settings/security/exchange-key-schema.ts`
- Create: `web/src/features/system-settings/security/exchange-key-settings-section.tsx`
- Create: `web/src/features/system-settings/security/__tests__/exchange-key-api.test.ts`
- Create: `web/src/features/system-settings/security/__tests__/exchange-key-schema.test.ts`
- Create: `web/src/features/system-settings/security/__tests__/exchange-key-settings-section.test.tsx`
- Modify: `web/src/features/system-settings/security/section-registry.tsx`
- Modify: locale files **only** via `web/scripts/add-missing-keys.mjs` then `bun run i18n:sync`

**Interfaces:**
- Consumes: `GET/PUT /api/option/exchange-key`（Task 2）。
- Produces:
  - `ExchangeKeySettingsView`：`enabled`、`secret_configured`、`secret_from_env`、`enabled_from_env`
  - `ExchangeKeySettingsUpdate`：`enabled`、`secret_key`、`secret_key_clear`
  - `fetchExchangeKeySettings()` / `updateExchangeKeySettings(payload)`
  - Section id `exchange-key`，`titleKey: 'Exchange Key'`，`build: () => <ExchangeKeySettingsSection />`（不走通用 option `defaultValues`）
  - SECRET 输入 `type="password"` `autoComplete="new-password"`，永不回显；Badge 用已有 `Configured` / `Not configured`
  - `secret_from_env`：禁用密钥输入与 clear；Alert 说明环境变量锁定
  - `enabled_from_env`：禁用开关
  - 空 `secret_key` 提交必须带 `secret_key: ''` 且 `secret_key_clear: false`（与 Langfuse「空=保持」一致）
  - 启用但未配置且输入为空：前端 zod 拦截（`Enter a secret before enabling Exchange Key`）
  - 非空 secret `< 16`：`Secret must be at least 16 characters`
  - clear 仅在 `enabled===false` 时展示，勾选且仍 enabled → `Disable Exchange Key before clearing the secret`

照抄 `langfuse-settings-section.test.tsx` 的 happy-dom + `SettingsPageProvider` + `QueryClient.setQueryData` 模式。测试命令用 `bun test`（CI 即如此）。

新 i18n 键（English source = key）：

- `Exchange Key`
- `Enable Exchange Key`
- `Allow internal applications to authenticate as an existing user with HMAC of the username. Charges use that user's wallet or subscription.`
- `Shared secret`
- `Configured by environment variable`
- `Enabled by environment variable`
- `Secret must be at least 16 characters`
- `Enter a secret before enabling Exchange Key`
- `Disable Exchange Key before clearing the secret`
- `Save Exchange Key settings`
- `EXCHANGE_KEY_SECRET is set on the server, so the secret cannot be changed here.`
- `EXCHANGE_KEY_ENABLED is set on the server, so this switch cannot be changed here.`

复用已有键：`Configured`、`Not configured`、`Leave empty to keep the stored secret. The secret is never sent back to this page.`、`Clear the stored secret key`、`Setting updated successfully`。

实现前先读 `.agents/skills/i18n-translate/SKILL.md`，locale 写入只走脚本。

- [ ] **Step 1: Write the failing frontend tests**

`exchange-key-api.test.ts`：mock `api.get`/`api.put`，断言 URL 为 `/api/option/exchange-key`；GET 失败 `success:false` 抛错。

`exchange-key-schema.test.ts`：

- 空 secret + `secret_configured:true` + enabled → parse 成功
- 空 secret + `secret_configured:false` + enabled → 失败
- secret 长度 15 → 失败；16 → 成功
- `secret_key_clear:true` 且 enabled → 失败

`exchange-key-settings-section.test.tsx`：

1. view `secret_from_env:true` → 密钥 input `disabled`，出现环境变量提示。
2. view `secret_configured:true`、输入保持空、点 Save → PUT payload `secret_key===''` 且 `secret_key_clear===false`。
3. `enabled_from_env:true` → 开关 `aria-disabled` 或 `data-disabled`。

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd web && bun test src/features/system-settings/security/__tests__/exchange-key-api.test.ts \
  src/features/system-settings/security/__tests__/exchange-key-schema.test.ts \
  src/features/system-settings/security/__tests__/exchange-key-settings-section.test.tsx
```

Expected: FAIL，模块不存在。

- [ ] **Step 3: Write the implementation**

`exchange-key-api.ts` 照 `langfuse-api.ts` 缩成四个字段。`exchange-key-schema.ts` 用 `z.object`，`superRefine` 里用 **传入的 persisted view**（不要在 schema 里闭包过期 view）：导出 `exchangeKeyFormSchema(view: ExchangeKeySettingsView)`。

Section：`useQuery` key `['exchange-key-settings']`，`useMutation` 成功 toast `Setting updated successfully` 并 invalidate。`SettingsPageFormActions` `saveLabel='Save Exchange Key settings'`。

`section-registry.tsx` 在 `token-limits` 后追加一节。新 web 文件带与其它 security 文件相同的 AGPL 文件头。

然后按 i18n skill：写临时 `web/scripts/add-missing-keys.mjs`，七语都填（不要把英文原样贴进 zh/fr/ja/ru/vi/zh-TW），执行：

```bash
cd web
node scripts/add-missing-keys.mjs
bun run i18n:sync
```

删掉临时脚本。

翻译（写入 `newKeys`，不要手改 json）：

| key | zh | zh-TW | fr | ja | ru | vi |
| --- | --- | --- | --- | --- | --- | --- |
| Exchange Key | Exchange Key | Exchange Key | Exchange Key | Exchange Key | Exchange Key | Exchange Key |
| Enable Exchange Key | 启用 Exchange Key | 啟用 Exchange Key | Activer Exchange Key | Exchange Key を有効化 | Включить Exchange Key | Bật Exchange Key |
| Allow internal applications... | 允许内部应用通过用户名 HMAC 以已有用户身份调用。扣费走该用户的钱包或订阅。 | 允許內部應用透過使用者名稱 HMAC 以既有使用者身分呼叫。扣費走該使用者的錢包或訂閱。 | Permet aux applications internes de s'authentifier comme un utilisateur existant via HMAC du nom. Les frais utilisent son portefeuille ou son abonnement. | 内部アプリがユーザー名の HMAC で既存ユーザーとして認証できます。課金はそのユーザーのウォレットまたはサブスクリプションを使います。 | Позволяет внутренним приложениям входить как существующий пользователь по HMAC имени. Списание идёт с кошелька или подписки этого пользователя. | Cho phép ứng dụng nội bộ xác thực với tư cách user đã có bằng HMAC của tên. Trừ phí từ ví hoặc gói của user đó. |
| Shared secret | 共享密钥 | 共用密鑰 | Secret partagé | 共有シークレット | Общий секрет | Khóa bí mật dùng chung |
| Configured by environment variable | 已由环境变量配置 | 已由環境變數設定 | Configuré par variable d'environnement | 環境変数で設定済み | Задано переменной окружения | Đã cấu hình bằng biến môi trường |
| Enabled by environment variable | 已由环境变量启用 | 已由環境變數啟用 | Activé par variable d'environnement | 環境変数で有効化 | Включено переменной окружения | Đã bật bằng biến môi trường |
| Secret must be at least 16 characters | 密钥至少 16 个字符 | 密鑰至少 16 個字元 | Le secret doit contenir au moins 16 caractères | シークレットは 16 文字以上 | Секрет не короче 16 символов | Secret phải dài ít nhất 16 ký tự |
| Enter a secret before enabling Exchange Key | 启用前请先填写密钥 | 啟用前請先填寫密鑰 | Saisissez un secret avant d'activer Exchange Key | Exchange Key を有効にする前にシークレットを入力してください | Введите секрет до включения Exchange Key | Nhập secret trước khi bật Exchange Key |
| Disable Exchange Key before clearing the secret | 请先关闭 Exchange Key 再清除密钥 | 請先關閉 Exchange Key 再清除密鑰 | Désactivez Exchange Key avant d'effacer le secret | シークレットを消す前に Exchange Key を無効にしてください | Сначала выключите Exchange Key, затем очистите секрет | Tắt Exchange Key trước khi xóa secret |
| Save Exchange Key settings | 保存 Exchange Key 设置 | 儲存 Exchange Key 設定 | Enregistrer Exchange Key | Exchange Key 設定を保存 | Сохранить Exchange Key | Lưu cài đặt Exchange Key |
| EXCHANGE_KEY_SECRET is set... | 服务器已设置 EXCHANGE_KEY_SECRET，无法在此修改密钥。 | 伺服器已設定 EXCHANGE_KEY_SECRET，無法在此修改密鑰。 | EXCHANGE_KEY_SECRET est défini sur le serveur ; le secret ne peut pas être modifié ici. | サーバーで EXCHANGE_KEY_SECRET が設定されているため、ここでは変更できません。 | EXCHANGE_KEY_SECRET задан на сервере, секрет здесь изменить нельзя. | EXCHANGE_KEY_SECRET đã được đặt trên máy chủ, không thể đổi secret tại đây. |
| EXCHANGE_KEY_ENABLED is set... | 服务器已设置 EXCHANGE_KEY_ENABLED，无法在此修改开关。 | 伺服器已設定 EXCHANGE_KEY_ENABLED，無法在此修改開關。 | EXCHANGE_KEY_ENABLED est défini sur le serveur ; ce commutateur ne peut pas être modifié ici. | サーバーで EXCHANGE_KEY_ENABLED が設定されているため、このスイッチは変更できません。 | EXCHANGE_KEY_ENABLED задан на сервере, этот переключатель нельзя изменить. | EXCHANGE_KEY_ENABLED đã được đặt trên máy chủ, không thể đổi công tắc này. |

「Allow internal applications...」行在脚本里必须用 **完整英文 key**，与 `t('...')` 字面量一致。

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd web
bun test src/features/system-settings/security/__tests__/
bun run typecheck
bun run i18n:sync
```

Expected: 测试 PASS；typecheck 无错；sync 不再报告这些新键缺失。

再跑后端回归：

```bash
GOWORK=off go test ./setting/exchange_key/ ./middleware/ ./controller/ ./service/ -run 'ExchangeKey|GetUserIDByUsername|AppliesTokenQuota|LimitedTokenStill|PreWss' -count=1
cd relaykit && GOWORK=off go build ./...
```

Expected: PASS / build OK。relaykit 无改动，只确认未误引入依赖。

- [ ] **Step 5: Commit**

```bash
git add web/src/features/system-settings/security/ \
  web/src/i18n/locales/
git commit -m "$(cat <<'EOF'
feat(web): add exchange-key controls on the security settings page

EOF
)"
```

不要 `git add` 临时 `add-missing-keys.mjs`（应已删除）。不要把 8850 主机上的真实 `EXCHANGE_KEY_SECRET` 提交进仓库。

部署（不进本任务代码）：镜像上线后现网零行为变化。在 `/home/flintylemming/appdata/8850-new-api` 的 compose 里用主机环境变量设置 `EXCHANGE_KEY_ENABLED` / `EXCHANGE_KEY_SECRET`（SECRET 只放该主机，不进 git），改 env 后重启容器。内部应用：`key = "sk-" + username + "-" + hex(HMAC-SHA256(secret, username))`。回滚：去掉 env 并 `enabled=false`。已产生的 `token_name=exchange-key` 日志保留。

---

## Self-review

1. **Spec coverage:** Parse/HMAC（T1）、env/option 与专用 API（T1–T2）、username `First`+软删（T2）、TokenAuth 顺序与虚拟上下文（T3）、ReadOnly usage/log-by-key（T3）、`AppliesTokenQuota` 全路径含 WSS（T4）、钱包扣费且无 token 行（T4）、安全页 UI 与七语（T5）、默认关闭与 compose 注释（T1/T2）、不改 relaykit / 不回传 SECRET / 不建 tokens 行 — 均有任务。威胁模型（持 SECRET 可花任意未封禁用户额度）是产品接受范围，不另写代码。
2. **Placeholder scan:** 无 TBD /「类似 Task N」/「补测试」。
3. **Type consistency:** `TokenName`、`OptionKeyPrefix`、`SettingView` JSON 字段、`UpdateRequest`、`ContextKeyExchangeKey`、`RelayInfo.IsExchangeKey`、`AppliesTokenQuota()` 在后续任务中名称一致。
