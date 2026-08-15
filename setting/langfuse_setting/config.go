// Package langfuse_setting holds the Langfuse tracing configuration, its
// validation rules and the immutable snapshot published to the data plane.
package langfuse_setting

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

// OptionKeyPrefix is the exact prefix every persisted Langfuse option key uses.
const OptionKeyPrefix = "langfuse_setting."

// TracesPath is the fixed OTLP traces path Langfuse exposes under the base URL.
const TracesPath = "/api/public/otel/v1/traces"

// Per-field bounds from design §10. Each maximum must stay reachable on its own
// when the other fields sit at their minimum, so the API never advertises a
// bound that the whole-group tuple check makes impossible to save.
const (
	minContentBytes = 4096
	maxContentBytes = 4194304

	minResponseBytes = 65536
	maxResponseBytes = 8388608

	minSessionBodyBytes = 1024
	maxSessionBodyBytes = 65536

	minQueueSize = 16
	maxQueueSize = 256

	minBatchSize = 1
	maxBatchSize = 32

	// Conservative finite range; the design only requires the flush interval to
	// have explicit bounds.
	minFlushIntervalSeconds = 1
	maxFlushIntervalSeconds = 300

	// Envelope headroom below the 9,500,000 decimal byte single-span warning
	// Langfuse emits, and the local body planning ceiling for the BSP queue,
	// the current batch and encode/compress buffers.
	maxQueuedSpanBytesLimit = 9_000_000
	// 2 GiB, deliberately below the 3,168,000,000 bytes (2.95 GiB) that the
	// worst legal tuple (queue 256, batch 32, span envelope 9,000,000) can plan.
	// A ceiling of 3 GiB or more could never be crossed, turning this rule into
	// dead code; 2 GiB still admits a 4 MiB content capture at a realistic
	// queue/batch while keeping the check able to reject an oversized one.
	queueBodyPlanningLimit int64 = 2 * 1024 * 1024 * 1024
)

// RFC 7230 token characters, the only ones allowed in an HTTP field name.
const httpTokenChars = "!#$%&'*+-.^_`|~" +
	"0123456789" +
	"abcdefghijklmnopqrstuvwxyz" +
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ"

var environmentPattern = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)

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

// NormalizeHost validates the configured Langfuse base URL and splits it into
// the parts the traces endpoint is built from. The base path is normalized to
// either "" (root) or a single leading slash without a trailing one.
func NormalizeHost(raw string) (string, string, string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", "", errors.New("host 必须是绝对 http/https URL")
	}
	if u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", "", "", errors.New("host 必须包含 host/port 且不允许 userinfo 或 opaque URL")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return "", "", "", errors.New("host 不允许 query 或 fragment")
	}
	// A non-empty RawPath means the escaped form differs from the decoded one,
	// which would make the appended traces path ambiguous.
	if u.RawPath != "" || !utf8.ValidString(u.Path) {
		return "", "", "", errors.New("host 不允许编码路径或非法 UTF-8")
	}
	for _, r := range u.Path {
		if r == '\\' || unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", "", "", errors.New("host path 含反斜杠、控制字符或空白")
		}
	}
	basePath := path.Clean("/" + strings.Trim(u.Path, "/"))
	if basePath == "/" {
		basePath = ""
	}
	// Appending the traces path to a base that already ends with it would
	// silently produce a doubled path, so reject instead of guessing intent.
	if strings.HasSuffix(basePath, TracesPath) {
		return "", "", "", errors.New("host 不应包含完整 traces 路径，只需填 base URL")
	}
	return u.Scheme, u.Host, basePath, nil // url.Parse already lower-cased the scheme
}

// BuildTracesURL assembles the OTLP endpoint the exporter posts spans to.
func BuildTracesURL(scheme, authority, basePath string) string {
	return (&url.URL{
		Scheme: scheme,
		Host:   authority,
		Path:   path.Join(basePath, TracesPath),
	}).String()
}

// Validate checks the configuration as one group: a single illegal field makes
// the whole update fail, so no partial configuration is ever persisted.
func Validate(s LangfuseSetting) error {
	if s.Host != "" {
		if _, _, _, err := NormalizeHost(s.Host); err != nil {
			return err
		}
	}
	if !environmentPattern.MatchString(s.Environment) {
		return errors.New("environment 必须匹配 ^[a-z0-9_-]{1,40}$")
	}
	if strings.HasPrefix(s.Environment, "langfuse") {
		return errors.New("environment 不能以 langfuse 开头")
	}
	if math.IsNaN(s.SampleRate) || s.SampleRate < 0 || s.SampleRate > 1 {
		return errors.New("sample_rate 必须位于 [0,1]")
	}
	if s.MaxContentBytes < minContentBytes || s.MaxContentBytes > maxContentBytes {
		return fmt.Errorf("max_content_bytes 必须位于 %d-%d", minContentBytes, maxContentBytes)
	}
	if s.MaxResponseBytes < minResponseBytes || s.MaxResponseBytes > maxResponseBytes {
		return fmt.Errorf("max_response_bytes 必须位于 %d-%d", minResponseBytes, maxResponseBytes)
	}
	if s.MaxSessionBodyBytes < minSessionBodyBytes || s.MaxSessionBodyBytes > maxSessionBodyBytes {
		return fmt.Errorf("max_session_body_bytes 必须位于 %d-%d", minSessionBodyBytes, maxSessionBodyBytes)
	}
	if s.QueueSize < minQueueSize || s.QueueSize > maxQueueSize {
		return fmt.Errorf("queue_size 必须位于 %d-%d", minQueueSize, maxQueueSize)
	}
	if s.BatchSize < minBatchSize || s.BatchSize > maxBatchSize {
		return fmt.Errorf("batch_size 必须位于 %d-%d", minBatchSize, maxBatchSize)
	}
	if s.BatchSize > s.QueueSize {
		return errors.New("batch_size 不能大于 queue_size")
	}
	if s.FlushIntervalSeconds < minFlushIntervalSeconds || s.FlushIntervalSeconds > maxFlushIntervalSeconds {
		return fmt.Errorf("flush_interval_seconds 必须位于 %d-%d", minFlushIntervalSeconds, maxFlushIntervalSeconds)
	}
	if s.MaxInFlightCaptureBytes <= 0 {
		return errors.New("max_in_flight_capture_bytes 必须为正")
	}

	// The single-request capture reservation and the per-span body budget share
	// the same expression: two content buffers plus one response buffer.
	doubledContent, ok := checkedMul(2, int64(s.MaxContentBytes))
	if !ok {
		return errors.New("max_content_bytes 溢出")
	}
	maxQueuedSpanBytes, ok := checkedAdd(doubledContent, int64(s.MaxResponseBytes))
	if !ok {
		return errors.New("max_content_bytes/max_response_bytes 组合溢出")
	}
	if maxQueuedSpanBytes > maxQueuedSpanBytesLimit {
		return fmt.Errorf("2*max_content_bytes+max_response_bytes 不得超过 %d 字节", maxQueuedSpanBytesLimit)
	}
	if int64(s.MaxInFlightCaptureBytes) < maxQueuedSpanBytes {
		return errors.New("max_in_flight_capture_bytes 不得小于单请求 reservation 2*max_content_bytes+max_response_bytes")
	}
	batchSlots, ok := checkedMul(3, int64(s.BatchSize))
	if !ok {
		return errors.New("batch_size 溢出")
	}
	plannedSlots, ok := checkedAdd(int64(s.QueueSize), batchSlots)
	if !ok {
		return errors.New("queue_size/batch_size 组合溢出")
	}
	plannedBytes, ok := checkedMul(plannedSlots, maxQueuedSpanBytes)
	if !ok {
		return errors.New("queue/batch 正文规划溢出")
	}
	if plannedBytes > queueBodyPlanningLimit {
		return fmt.Errorf("(queue_size+3*batch_size)*(2*max_content_bytes+max_response_bytes) 不得超过 %d 字节", queueBodyPlanningLimit)
	}

	for _, name := range s.SessionHeaderNames {
		if name == "" {
			return errors.New("session_header_names 不能包含空 Header 名")
		}
		if strings.ContainsFunc(name, func(r rune) bool { return !strings.ContainsRune(httpTokenChars, r) }) {
			return fmt.Errorf("session_header_names 含非法 HTTP Header 名: %s", name)
		}
		switch strings.ToLower(name) {
		case "authorization", "cookie", "proxy-authorization", "set-cookie":
			return fmt.Errorf("session_header_names 不允许配置凭证 Header: %s", name)
		}
	}
	for _, bodyPath := range s.SessionBodyPaths {
		if bodyPath == "" || strings.TrimSpace(bodyPath) != bodyPath {
			return errors.New("session_body_paths 必须是非空且无前后空白的路径")
		}
	}

	if s.Enabled {
		if s.Host == "" {
			return errors.New("启用 Langfuse 时 host 不能为空")
		}
		if s.PublicKey == "" || s.SecretKey == "" {
			return errors.New("启用 Langfuse 时 public_key 与 secret_key 均不能为空")
		}
	}
	return nil
}

func checkedAdd(a, b int64) (int64, bool) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, false
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, false
	}
	return a + b, true
}

func checkedMul(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	product := a * b
	if product/b != a {
		return 0, false
	}
	return product, true
}

// Snapshot is the immutable, validated configuration the data plane reads. It
// carries the pre-resolved endpoint parts so the relay hot path never parses a
// URL, plus a monotonic version used for diagnostics only.
type Snapshot struct {
	LangfuseSetting
	Scheme    string
	Authority string
	BasePath  string
	TracesURL string
	Version   uint64
}

// BuildSnapshot validates a candidate configuration and freezes it, deep
// copying every slice so later mutation of the source is not observable.
func BuildSnapshot(s LangfuseSetting, version uint64) (Snapshot, error) {
	if err := Validate(s); err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{LangfuseSetting: s, Version: version}
	snapshot.SessionHeaderNames = append([]string{}, s.SessionHeaderNames...)
	snapshot.SessionBodyPaths = append([]string{}, s.SessionBodyPaths...)
	if s.Host != "" {
		scheme, authority, basePath, err := NormalizeHost(s.Host)
		if err != nil {
			return Snapshot{}, err
		}
		snapshot.Scheme = scheme
		snapshot.Authority = authority
		snapshot.BasePath = basePath
		snapshot.TracesURL = BuildTracesURL(scheme, authority, basePath)
	}
	return snapshot, nil
}

// EqualConfig reports whether two snapshots carry the same configuration,
// ignoring the diagnostic version. Reconcile uses it to skip republishing an
// unchanged configuration, which would otherwise rebuild the exporter.
func (a Snapshot) EqualConfig(b Snapshot) bool {
	a.Version = 0
	b.Version = 0
	return reflect.DeepEqual(a, b)
}
