package exchange_key

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

const (
	OptionKeyPrefix = "exchange_key."
	MinSecretLength = 16
	EnvEnabled      = "EXCHANGE_KEY_ENABLED"
	EnvSecret       = "EXCHANGE_KEY_SECRET"
)

var (
	ErrSecretTooShort          = errors.New("SECRET 长度不能少于 16 个字符")
	ErrSecretRequired          = errors.New("启用 Exchange Key 时必须配置 SECRET")
	ErrSecretClearWhileEnabled = errors.New("只有在关闭 Exchange Key 后才能清除 SECRET")
	ErrSecretLockedByEnv       = errors.New("SECRET 已由环境变量 EXCHANGE_KEY_SECRET 配置，无法在后台修改")
	ErrEnabledLockedByEnv      = errors.New("开关已由环境变量 EXCHANGE_KEY_ENABLED 配置，无法在后台修改")

	stored = &Setting{}

	warnMu     sync.Mutex
	lastWarned string
)

type Setting struct {
	Enabled bool   `json:"enabled"`
	Secret  string `json:"secret"`
}

type EffectiveConfig struct {
	Enabled          bool
	SecretConfigured bool
	EnabledFromEnv   bool
	SecretFromEnv    bool
	Secret           string
}

type SettingView struct {
	Enabled          bool `json:"enabled"`
	SecretConfigured bool `json:"secret_configured"`
	SecretFromEnv    bool `json:"secret_from_env"`
	EnabledFromEnv   bool `json:"enabled_from_env"`
}

type UpdateRequest struct {
	Enabled        *bool  `json:"enabled"`
	SecretKey      string `json:"secret_key"`
	SecretKeyClear bool   `json:"secret_key_clear"`
}

func init() {
	config.GlobalConfig.Register("exchange_key", stored)
}

func Effective() EffectiveConfig {
	return readEnv(*stored)
}

func IsEffective() bool {
	effective := Effective()
	return effective.Enabled && effective.SecretConfigured
}

func BuildView() SettingView {
	effective := Effective()
	return SettingView{
		Enabled:          effective.Enabled,
		SecretConfigured: effective.SecretConfigured,
		SecretFromEnv:    effective.SecretFromEnv,
		EnabledFromEnv:   effective.EnabledFromEnv,
	}
}

func ApplyUpdate(current Setting, req UpdateRequest) (Setting, error) {
	effective := readEnv(current)
	candidate := current

	if effective.EnabledFromEnv && req.Enabled != nil {
		if *req.Enabled != effective.Enabled {
			return Setting{}, ErrEnabledLockedByEnv
		}
	} else if req.Enabled != nil {
		candidate.Enabled = *req.Enabled
	}

	if effective.SecretFromEnv && (req.SecretKey != "" || req.SecretKeyClear) {
		return Setting{}, ErrSecretLockedByEnv
	}
	if req.SecretKey != "" {
		if len(req.SecretKey) < MinSecretLength {
			return Setting{}, ErrSecretTooShort
		}
		candidate.Secret = req.SecretKey
	}
	if req.SecretKeyClear {
		if candidate.Enabled {
			return Setting{}, ErrSecretClearWhileEnabled
		}
		candidate.Secret = ""
	}

	candidateEffective := readEnv(candidate)
	if candidateEffective.Enabled && !candidateEffective.SecretConfigured {
		return Setting{}, ErrSecretRequired
	}
	return candidate, nil
}

func GetStored() Setting {
	return *stored
}

func ReplaceStoredForTest(t *testing.T, s Setting) {
	t.Helper()
	old := *stored
	*stored = s
	t.Cleanup(func() {
		*stored = old
	})
}

func LogMisconfiguredEnv() {
	_ = readEnv(*stored)
}

func readEnv(current Setting) EffectiveConfig {
	effective := EffectiveConfig{
		Enabled: current.Enabled,
		Secret:  current.Secret,
	}
	var warningKeys, warnings []string
	if len(effective.Secret) < MinSecretLength {
		effective.Secret = ""
	}

	if raw, ok := os.LookupEnv(EnvEnabled); ok && raw != "" {
		switch raw {
		case "true":
			effective.Enabled = true
			effective.EnabledFromEnv = true
		case "false":
			effective.Enabled = false
			effective.EnabledFromEnv = true
		default:
			warningKeys = append(warningKeys, EnvEnabled+"\x00"+raw)
			warnings = append(warnings, fmt.Sprintf("%s 只能设置为 true 或 false，当前值无效", EnvEnabled))
		}
	}

	if raw, ok := os.LookupEnv(EnvSecret); ok && raw != "" {
		effective.SecretFromEnv = true
		if len(raw) < MinSecretLength {
			effective.Secret = ""
			warningKeys = append(warningKeys, EnvSecret+"\x00"+raw)
			warnings = append(warnings, fmt.Sprintf("%s 长度不能少于 %d 个字符，Exchange Key 将保持不可用", EnvSecret, MinSecretLength))
		} else {
			effective.Secret = raw
		}
	}

	if len(warnings) != 0 {
		warnOnce(strings.Join(warningKeys, "\x01"), strings.Join(warnings, "; "))
	}
	effective.SecretConfigured = effective.Secret != ""
	return effective
}

func warnOnce(key, message string) {
	warnMu.Lock()
	defer warnMu.Unlock()
	if key == lastWarned {
		return
	}
	lastWarned = key
	common.SysError(message)
}
