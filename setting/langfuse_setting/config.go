// Package langfuse_setting holds the Langfuse tracing configuration, its
// validation rules and the immutable snapshot published to the data plane.
package langfuse_setting

import (
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

// OptionKeyPrefix is the exact prefix every persisted Langfuse option key uses.
const OptionKeyPrefix = "langfuse_setting."

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

// DefaultLangfuseSetting is the disabled baseline. The sample_rate/send_content
// defaults only serve disabled bootstrap, backfill of older deployments and UI
// drafts; they never constitute silent enablement (see design §10).
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

// registered exists only so the generic options plumbing keeps persisting and
// exporting langfuse_setting.* keys. It is mutated per key by updateOptionMap
// and must never be read by relay, the dedicated GET handler or the runtime
// manager — those read the immutable snapshot instead (design §10.2).
var registered = DefaultLangfuseSetting

func init() {
	config.GlobalConfig.Register("langfuse_setting", &registered)
}

// SettingFromOptionMap rebuilds a complete setting from persisted option rows,
// using the defaults as the base so a deployment missing keys still yields a
// full, coherent configuration. Unparsable values keep their default.
func SettingFromOptionMap(m map[string]string) LangfuseSetting {
	out := DefaultLangfuseSetting
	out.SessionHeaderNames = slices.Clone(DefaultLangfuseSetting.SessionHeaderNames)
	out.SessionBodyPaths = slices.Clone(DefaultLangfuseSetting.SessionBodyPaths)

	val := reflect.ValueOf(&out).Elem()
	typ := val.Type()
	for key, raw := range m {
		tag, ok := strings.CutPrefix(key, OptionKeyPrefix)
		if !ok {
			continue
		}
		fieldIndex := -1
		for i := 0; i < typ.NumField(); i++ {
			if typ.Field(i).Tag.Get("json") == tag {
				fieldIndex = i
				break
			}
		}
		if fieldIndex < 0 {
			continue
		}
		field := val.Field(fieldIndex)
		switch field.Kind() {
		case reflect.String:
			field.SetString(raw)
		case reflect.Bool:
			if parsed, err := strconv.ParseBool(raw); err == nil {
				field.SetBool(parsed)
			}
		case reflect.Int:
			if parsed, err := strconv.Atoi(raw); err == nil {
				field.SetInt(int64(parsed))
			}
		case reflect.Float64:
			if parsed, err := strconv.ParseFloat(raw, 64); err == nil {
				field.SetFloat(parsed)
			}
		case reflect.Slice:
			// String slices are stored as JSON arrays by setting/config.configToMap.
			var parsed []string
			if err := common.UnmarshalJsonStr(raw, &parsed); err == nil {
				field.Set(reflect.ValueOf(parsed))
			}
		}
	}
	return out
}
