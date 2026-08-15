// Package langfuseconfig is the Langfuse control plane. It is the only place
// that reads and writes the persisted langfuse_setting.* options, rebuilds a
// candidate from the complete set and publishes it to the data plane. Relay
// code must never import it.
package langfuseconfig

import (
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/langfuse"
	"github.com/QuantumNous/new-api/setting/langfuse_setting"
)

// ErrStorage marks a database failure, as opposed to a rejected request the
// administrator can fix by changing the payload.
var ErrStorage = errors.New("Langfuse 配置读写失败")

// ErrEnableConfirmationRequired is returned when a disabled configuration is
// switched on without an explicit sampling and content decision. Falling back
// to the defaults here would silently start sending prompts to Langfuse.
var ErrEnableConfirmationRequired = errors.New("启用 Langfuse 时必须显式提交 sample_rate 与 send_content")

// ErrSecretClearWhileEnabled is returned when a secret wipe is requested for a
// configuration that stays enabled, which would break the exporter.
var ErrSecretClearWhileEnabled = errors.New("只有在关闭 Langfuse 后才能清除 secret_key")

// configMutex spans the whole read-validate-persist-publish sequence of both
// Update and Reconcile, so a slower poll cannot republish a stale configuration
// after a newer PUT already committed.
var configMutex sync.Mutex

// UpdateRequest is the parsed body of PUT /api/option/langfuse. sample_rate,
// send_content and enabled keep JSON field presence so the enable confirmation
// rule can distinguish "unchanged" from "explicitly chosen".
type UpdateRequest struct {
	Enabled                 *bool    `json:"enabled"`
	Host                    string   `json:"host"`
	PublicKey               string   `json:"public_key"`
	SecretKey               string   `json:"secret_key"`       // "" keeps the stored secret
	SecretKeyClear          bool     `json:"secret_key_clear"` // only legal when the result is disabled
	Environment             string   `json:"environment"`
	SampleRate              *float64 `json:"sample_rate"`
	SendContent             *bool    `json:"send_content"`
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

// SettingView is the GET /api/option/langfuse response. The secret key is
// reported only as a presence flag and never leaves the process.
type SettingView struct {
	Enabled                 bool     `json:"enabled"`
	Host                    string   `json:"host"`
	PublicKey               string   `json:"public_key"`
	SecretKeyConfigured     bool     `json:"secret_key_configured"`
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

// GetView reports the persisted configuration for the admin UI.
func GetView() (SettingView, error) {
	configMutex.Lock()
	defer configMutex.Unlock()

	persisted, err := loadPersisted()
	if err != nil {
		return SettingView{}, err
	}
	return SettingView{
		Enabled:                 persisted.Enabled,
		Host:                    persisted.Host,
		PublicKey:               persisted.PublicKey,
		SecretKeyConfigured:     persisted.SecretKey != "",
		Environment:             persisted.Environment,
		SampleRate:              persisted.SampleRate,
		SendContent:             persisted.SendContent,
		MaxContentBytes:         persisted.MaxContentBytes,
		MaxResponseBytes:        persisted.MaxResponseBytes,
		MaxInFlightCaptureBytes: persisted.MaxInFlightCaptureBytes,
		MaxSessionBodyBytes:     persisted.MaxSessionBodyBytes,
		SessionHeaderNames:      normalizeStrings(persisted.SessionHeaderNames),
		SessionBodyPaths:        normalizeStrings(persisted.SessionBodyPaths),
		QueueSize:               persisted.QueueSize,
		BatchSize:               persisted.BatchSize,
		FlushIntervalSeconds:    persisted.FlushIntervalSeconds,
	}, nil
}

// Update applies a whole-group configuration change: the candidate is built
// from the persisted state plus the request, validated as one group, saved in a
// single transaction and only then published. A rejected or failed update
// leaves both the database and the active binding untouched.
func Update(req UpdateRequest) error {
	configMutex.Lock()
	defer configMutex.Unlock()

	persisted, err := loadPersisted()
	if err != nil {
		return err
	}

	candidate := langfuse_setting.LangfuseSetting{
		Enabled:                 persisted.Enabled,
		Host:                    req.Host,
		PublicKey:               req.PublicKey,
		SecretKey:               persisted.SecretKey,
		Environment:             req.Environment,
		SampleRate:              persisted.SampleRate,
		SendContent:             persisted.SendContent,
		MaxContentBytes:         req.MaxContentBytes,
		MaxResponseBytes:        req.MaxResponseBytes,
		MaxInFlightCaptureBytes: req.MaxInFlightCaptureBytes,
		MaxSessionBodyBytes:     req.MaxSessionBodyBytes,
		SessionHeaderNames:      normalizeStrings(req.SessionHeaderNames),
		SessionBodyPaths:        normalizeStrings(req.SessionBodyPaths),
		QueueSize:               req.QueueSize,
		BatchSize:               req.BatchSize,
		FlushIntervalSeconds:    req.FlushIntervalSeconds,
	}
	if req.Enabled != nil {
		candidate.Enabled = *req.Enabled
	}
	if req.SampleRate != nil {
		candidate.SampleRate = *req.SampleRate
	}
	if req.SendContent != nil {
		candidate.SendContent = *req.SendContent
	}
	if req.SecretKey != "" {
		candidate.SecretKey = req.SecretKey
	}
	if req.SecretKeyClear {
		if candidate.Enabled {
			return ErrSecretClearWhileEnabled
		}
		candidate.SecretKey = ""
	}
	// Turning tracing on always requires a fresh privacy and capacity decision,
	// including after it was switched off and back on.
	if !persisted.Enabled && candidate.Enabled && (req.SampleRate == nil || req.SendContent == nil) {
		return ErrEnableConfirmationRequired
	}
	if err := langfuse_setting.Validate(candidate); err != nil {
		return err
	}

	values, err := optionValues(candidate)
	if err != nil {
		return err
	}
	if err := model.UpdateOptionsBulk(values); err != nil {
		return fmt.Errorf("%w: %v", ErrStorage, err)
	}
	return langfuse.PublishSnapshot(candidate)
}

// Reconcile rebuilds the candidate from the complete persisted set and
// publishes it when it differs from the active snapshot. Reading a partially
// written set is impossible because Update commits all keys in one transaction;
// an invalid set only warns and keeps the previous binding.
func Reconcile() error {
	configMutex.Lock()
	defer configMutex.Unlock()

	persisted, err := loadPersisted()
	if err != nil {
		return err
	}
	candidate, err := langfuse_setting.BuildSnapshot(persisted, 0)
	if err != nil {
		return err
	}
	if candidate.EqualConfig(langfuse.LoadBinding().Snapshot) {
		return nil
	}
	return langfuse.PublishSnapshot(persisted)
}

// StartReconcileLoop keeps this instance aligned with the database, which is
// how a configuration change on another node propagates here.
func StartReconcileLoop(frequencySeconds int) {
	if frequencySeconds <= 0 {
		frequencySeconds = 60
	}
	var lastErrorLog time.Time
	for {
		time.Sleep(time.Duration(frequencySeconds) * time.Second)
		if err := Reconcile(); err != nil {
			// A persistently invalid stored configuration would otherwise log
			// on every tick.
			if time.Since(lastErrorLog) >= 5*time.Minute {
				lastErrorLog = time.Now()
				common.SysError("langfuse reconcile failed: " + err.Error())
			}
		}
	}
}

func loadPersisted() (langfuse_setting.LangfuseSetting, error) {
	options, err := model.AllOptionsByPrefix(langfuse_setting.OptionKeyPrefix)
	if err != nil {
		return langfuse_setting.LangfuseSetting{}, fmt.Errorf("%w: %v", ErrStorage, err)
	}
	return langfuse_setting.SettingFromOptionMap(options), nil
}

// normalizeStrings keeps a nil slice out of both the persisted representation
// and the API response, so `[]` is the single encoding of "nothing configured".
func normalizeStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func optionValues(s langfuse_setting.LangfuseSetting) (map[string]string, error) {
	headerNames, err := common.Marshal(s.SessionHeaderNames)
	if err != nil {
		return nil, err
	}
	bodyPaths, err := common.Marshal(s.SessionBodyPaths)
	if err != nil {
		return nil, err
	}
	prefix := langfuse_setting.OptionKeyPrefix
	return map[string]string{
		prefix + "enabled":                     strconv.FormatBool(s.Enabled),
		prefix + "host":                        s.Host,
		prefix + "public_key":                  s.PublicKey,
		prefix + "secret_key":                  s.SecretKey,
		prefix + "environment":                 s.Environment,
		prefix + "sample_rate":                 strconv.FormatFloat(s.SampleRate, 'f', -1, 64),
		prefix + "send_content":                strconv.FormatBool(s.SendContent),
		prefix + "max_content_bytes":           strconv.Itoa(s.MaxContentBytes),
		prefix + "max_response_bytes":          strconv.Itoa(s.MaxResponseBytes),
		prefix + "max_in_flight_capture_bytes": strconv.Itoa(s.MaxInFlightCaptureBytes),
		prefix + "max_session_body_bytes":      strconv.Itoa(s.MaxSessionBodyBytes),
		prefix + "session_header_names":        string(headerNames),
		prefix + "session_body_paths":          string(bodyPaths),
		prefix + "queue_size":                  strconv.Itoa(s.QueueSize),
		prefix + "batch_size":                  strconv.Itoa(s.BatchSize),
		prefix + "flush_interval_seconds":      strconv.Itoa(s.FlushIntervalSeconds),
	}, nil
}
