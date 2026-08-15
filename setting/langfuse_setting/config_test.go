package langfuse_setting

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSettingFromOptionMapEmptyReturnsDefaults(t *testing.T) {
	got := SettingFromOptionMap(map[string]string{})

	assert.False(t, got.Enabled)
	assert.Equal(t, "", got.Host)
	assert.Equal(t, "", got.PublicKey)
	assert.Equal(t, "", got.SecretKey)
	assert.Equal(t, "default", got.Environment)
	assert.Equal(t, 0.1, got.SampleRate)
	assert.False(t, got.SendContent)
	assert.Equal(t, 65536, got.MaxContentBytes)
	assert.Equal(t, 524288, got.MaxResponseBytes)
	assert.Equal(t, 536870912, got.MaxInFlightCaptureBytes)
	assert.Equal(t, 65536, got.MaxSessionBodyBytes)
	assert.Empty(t, got.SessionHeaderNames)
	assert.Empty(t, got.SessionBodyPaths)
	assert.Equal(t, 64, got.QueueSize)
	assert.Equal(t, 16, got.BatchSize)
	assert.Equal(t, 5, got.FlushIntervalSeconds)
}

func TestSettingFromOptionMapOverlaysPrefixedKeys(t *testing.T) {
	got := SettingFromOptionMap(map[string]string{
		"langfuse_setting.enabled":              "true",
		"langfuse_setting.host":                 "http://langfuse:3000",
		"langfuse_setting.public_key":           "pk-lf-1",
		"langfuse_setting.secret_key":           "sk-lf-1",
		"langfuse_setting.sample_rate":          "0.25",
		"langfuse_setting.send_content":         "true",
		"langfuse_setting.max_content_bytes":    "8192",
		"langfuse_setting.session_header_names": `["X-Conversation-Id"]`,
		"langfuse_setting.session_body_paths":   `["metadata.session_id"]`,
		"langfuse_setting.queue_size":           "128",
		// 非 langfuse 前缀的 key 必须被忽略。
		"other_setting.queue_size": "999",
	})

	assert.True(t, got.Enabled)
	assert.Equal(t, "http://langfuse:3000", got.Host)
	assert.Equal(t, "pk-lf-1", got.PublicKey)
	assert.Equal(t, "sk-lf-1", got.SecretKey)
	assert.Equal(t, 0.25, got.SampleRate)
	assert.True(t, got.SendContent)
	assert.Equal(t, 8192, got.MaxContentBytes)
	assert.Equal(t, []string{"X-Conversation-Id"}, got.SessionHeaderNames)
	assert.Equal(t, []string{"metadata.session_id"}, got.SessionBodyPaths)
	assert.Equal(t, 128, got.QueueSize)
	// 未出现的 key 保持默认值。
	assert.Equal(t, "default", got.Environment)
	assert.Equal(t, 524288, got.MaxResponseBytes)
	assert.Equal(t, 16, got.BatchSize)
}

func TestSettingFromOptionMapKeepsDefaultOnUnparsableValue(t *testing.T) {
	got := SettingFromOptionMap(map[string]string{
		"langfuse_setting.sample_rate":          "not-a-float",
		"langfuse_setting.queue_size":           "not-an-int",
		"langfuse_setting.enabled":              "not-a-bool",
		"langfuse_setting.session_header_names": "not-json",
		"langfuse_setting.unknown_field":        "ignored",
	})

	assert.Equal(t, 0.1, got.SampleRate)
	assert.Equal(t, 64, got.QueueSize)
	assert.False(t, got.Enabled)
	assert.Empty(t, got.SessionHeaderNames)
}

func TestSettingFromOptionMapDoesNotMutateDefaults(t *testing.T) {
	got := SettingFromOptionMap(map[string]string{
		"langfuse_setting.queue_size":           "256",
		"langfuse_setting.session_header_names": `["X-A"]`,
	})
	require.Equal(t, 256, got.QueueSize)

	assert.Equal(t, 64, DefaultLangfuseSetting.QueueSize)
	assert.Empty(t, DefaultLangfuseSetting.SessionHeaderNames)
}

func TestNormalizeHost(t *testing.T) {
	cases := []struct {
		name          string
		raw           string
		wantScheme    string
		wantAuthority string
		wantBasePath  string
		wantTracesURL string
	}{
		{
			name:          "bare host with port",
			raw:           "http://langfuse:3000",
			wantScheme:    "http",
			wantAuthority: "langfuse:3000",
			wantBasePath:  "",
			wantTracesURL: "http://langfuse:3000/api/public/otel/v1/traces",
		},
		{
			name:          "sub path with trailing slash",
			raw:           "https://x.example/langfuse/",
			wantScheme:    "https",
			wantAuthority: "x.example",
			wantBasePath:  "/langfuse",
			wantTracesURL: "https://x.example/langfuse/api/public/otel/v1/traces",
		},
		{
			name:          "duplicated separators are cleaned",
			raw:           "https://x.example//a//b",
			wantScheme:    "https",
			wantAuthority: "x.example",
			wantBasePath:  "/a/b",
			wantTracesURL: "https://x.example/a/b/api/public/otel/v1/traces",
		},
		{
			name:          "ipv6 literal keeps brackets",
			raw:           "http://[::1]:3000",
			wantScheme:    "http",
			wantAuthority: "[::1]:3000",
			wantBasePath:  "",
			wantTracesURL: "http://[::1]:3000/api/public/otel/v1/traces",
		},
		{
			name:          "default port host",
			raw:           "https://x.example",
			wantScheme:    "https",
			wantAuthority: "x.example",
			wantBasePath:  "",
			wantTracesURL: "https://x.example/api/public/otel/v1/traces",
		},
		{
			name:          "scheme is lower cased",
			raw:           "  HTTPS://x.example/  ",
			wantScheme:    "https",
			wantAuthority: "x.example",
			wantBasePath:  "",
			wantTracesURL: "https://x.example/api/public/otel/v1/traces",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme, authority, basePath, err := NormalizeHost(tc.raw)
			require.NoError(t, err)
			assert.Equal(t, tc.wantScheme, scheme)
			assert.Equal(t, tc.wantAuthority, authority)
			assert.Equal(t, tc.wantBasePath, basePath)
			assert.Equal(t, tc.wantTracesURL, BuildTracesURL(scheme, authority, basePath))
		})
	}
}

func TestNormalizeHostRejects(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"unsupported scheme", "ftp://x"},
		{"missing scheme", "x.example"},
		{"scheme relative", "//x.example"},
		{"missing authority", "http:///path"},
		{"userinfo", "https://u:p@x"},
		{"query", "https://x?q=1"},
		{"forced empty query", "https://x/path?"},
		{"fragment", "https://x#f"},
		{"encoded path", "https://x/a%2Fb"},
		{"backslash in path", "https://x/a\\b"},
		{"space in path", "https://x/a b"},
		{"full traces path", "https://x/api/public/otel/v1/traces"},
		{"full traces path with trailing slash", "https://x/langfuse/api/public/otel/v1/traces/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := NormalizeHost(tc.raw)
			assert.Error(t, err)
		})
	}
}

// validSetting is the default configuration turned into a valid enabled one,
// so each validation case only has to state the field it changes.
func validSetting() LangfuseSetting {
	s := DefaultLangfuseSetting
	s.Enabled = true
	s.Host = "http://langfuse:3000"
	s.PublicKey = "pk-lf-1"
	s.SecretKey = "sk-lf-1"
	return s
}

func TestValidateAccepts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*LangfuseSetting)
	}{
		{"disabled defaults", func(s *LangfuseSetting) { *s = DefaultLangfuseSetting }},
		// reservation = 2*65536 + 524288 = 655360 (640 KiB) <= 536870912;
		// max_queued_span_bytes = 655360 <= 9,000,000;
		// (64 + 3*16) * 655360 = 73,400,320 <= 2 GiB.
		{"enabled defaults", func(s *LangfuseSetting) {}},
		{"sample rate zero pauses collection", func(s *LangfuseSetting) { s.SampleRate = 0 }},
		{"sample rate one", func(s *LangfuseSetting) { s.SampleRate = 1 }},
		// Every declared per-field maximum must be reachable with the other
		// fields at their minimum, otherwise the API would advertise a bound
		// that can never be saved.
		// 2*4194304 + 65536 = 8,454,144 <= 9,000,000;
		// (16 + 3*1) * 8,454,144 = 160,628,736 <= 2 GiB.
		{"max content bytes reachable", func(s *LangfuseSetting) {
			s.MaxContentBytes = 4194304
			s.MaxResponseBytes = 65536
			s.QueueSize = 16
			s.BatchSize = 1
		}},
		// 2*4096 + 8388608 = 8,396,800 <= 9,000,000;
		// (16 + 3*1) * 8,396,800 = 159,539,200 <= 2 GiB.
		{"max response bytes reachable", func(s *LangfuseSetting) {
			s.MaxContentBytes = 4096
			s.MaxResponseBytes = 8388608
			s.QueueSize = 16
			s.BatchSize = 1
		}},
		{"capture budget may equal reservation", func(s *LangfuseSetting) {
			s.MaxInFlightCaptureBytes = 2*s.MaxContentBytes + s.MaxResponseBytes
		}},
		{"session limits at bounds", func(s *LangfuseSetting) { s.MaxSessionBodyBytes = 1024 }},
		{"flush interval bounds", func(s *LangfuseSetting) { s.FlushIntervalSeconds = 300 }},
		{"batch equal to queue", func(s *LangfuseSetting) { s.QueueSize = 32; s.BatchSize = 32 }},
		{"session headers and paths", func(s *LangfuseSetting) {
			s.SessionHeaderNames = []string{"X-Conversation-Id", "X-Session-Id"}
			s.SessionBodyPaths = []string{"metadata.session_id", "chat_id"}
		}},
		{"environment charset", func(s *LangfuseSetting) { s.Environment = "prod-eu_1" }},
		{"disabled keeps empty keys", func(s *LangfuseSetting) {
			s.Enabled = false
			s.PublicKey = ""
			s.SecretKey = ""
			s.Host = ""
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validSetting()
			tc.mutate(&s)
			assert.NoError(t, Validate(s))
		})
	}
}

// The audit-completeness configuration this deployment runs: a 4 MiB content
// capture keeps long-context prompts whole instead of truncating them. It is
// the contract behind the raised ceilings, so it must stay saveable.
func TestValidateAcceptsAuditContentCapture(t *testing.T) {
	s := validSetting()
	s.SendContent = true
	s.MaxContentBytes = 4194304
	s.MaxResponseBytes = 524288
	s.QueueSize = 128
	s.BatchSize = 4
	s.MaxInFlightCaptureBytes = 8589934592

	require.NoError(t, Validate(s))

	// reservation = 2*4194304 + 524288 = 8,912,896 <= 9,000,000 envelope;
	// (128 + 3*4) * 8,912,896 = 1,247,805,440 <= 2 GiB planning ceiling.
	reservation := 2*s.MaxContentBytes + s.MaxResponseBytes
	assert.Equal(t, 8912896, reservation)
	assert.LessOrEqual(t, int64(reservation), int64(maxQueuedSpanBytesLimit))
	assert.LessOrEqual(t, int64(s.QueueSize+3*s.BatchSize)*int64(reservation), queueBodyPlanningLimit)
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*LangfuseSetting)
	}{
		{"content bytes below range", func(s *LangfuseSetting) { s.MaxContentBytes = 4095 }},
		{"content bytes above range", func(s *LangfuseSetting) { s.MaxContentBytes = 4194305 }},
		{"response bytes below range", func(s *LangfuseSetting) { s.MaxResponseBytes = 65535 }},
		{"response bytes above range", func(s *LangfuseSetting) { s.MaxResponseBytes = 8388609 }},
		{"session body bytes below range", func(s *LangfuseSetting) { s.MaxSessionBodyBytes = 1023 }},
		{"session body bytes above range", func(s *LangfuseSetting) { s.MaxSessionBodyBytes = 65537 }},
		{"queue size below range", func(s *LangfuseSetting) { s.QueueSize = 15 }},
		{"queue size above range", func(s *LangfuseSetting) { s.QueueSize = 257 }},
		{"batch size below range", func(s *LangfuseSetting) { s.BatchSize = 0 }},
		{"batch size above range", func(s *LangfuseSetting) { s.BatchSize = 33 }},
		{"batch larger than queue", func(s *LangfuseSetting) { s.QueueSize = 16; s.BatchSize = 17 }},
		// 1-300s is the conservative bound this plan introduces; the design
		// only requires an explicit finite range for the flush interval.
		{"flush interval below range", func(s *LangfuseSetting) { s.FlushIntervalSeconds = 0 }},
		{"flush interval above range", func(s *LangfuseSetting) { s.FlushIntervalSeconds = 301 }},
		// 2*4194304 + 8388608 = 16,777,216 > 9,000,000 single-span envelope.
		{"single span envelope exceeded", func(s *LangfuseSetting) {
			s.MaxContentBytes = 4194304
			s.MaxResponseBytes = 8388608
		}},
		// Span envelope stays legal (2*4194304 + 524288 = 8,912,896 <= 9,000,000)
		// but (256 + 3*32) * 8,912,896 = 3,137,339,392 blows the 2 GiB
		// queue/batch body planning bound.
		{"queue body planning exceeded", func(s *LangfuseSetting) {
			s.MaxContentBytes = 4194304
			s.MaxResponseBytes = 524288
			s.QueueSize = 256
			s.BatchSize = 32
		}},
		{"capture budget below reservation", func(s *LangfuseSetting) {
			s.MaxInFlightCaptureBytes = 2*s.MaxContentBytes + s.MaxResponseBytes - 1
		}},
		{"capture budget zero", func(s *LangfuseSetting) { s.MaxInFlightCaptureBytes = 0 }},
		{"capture budget negative", func(s *LangfuseSetting) { s.MaxInFlightCaptureBytes = -1 }},
		{"sample rate below range", func(s *LangfuseSetting) { s.SampleRate = -0.1 }},
		{"sample rate above range", func(s *LangfuseSetting) { s.SampleRate = 1.1 }},
		{"environment empty", func(s *LangfuseSetting) { s.Environment = "" }},
		{"environment reserved prefix", func(s *LangfuseSetting) { s.Environment = "langfuse-prod" }},
		{"environment upper case", func(s *LangfuseSetting) { s.Environment = "A_b" }},
		{"environment too long", func(s *LangfuseSetting) { s.Environment = strings.Repeat("a", 41) }},
		{"enabled without public key", func(s *LangfuseSetting) { s.PublicKey = "" }},
		{"enabled without secret key", func(s *LangfuseSetting) { s.SecretKey = "" }},
		{"enabled without host", func(s *LangfuseSetting) { s.Host = "" }},
		{"invalid host", func(s *LangfuseSetting) { s.Host = "langfuse:3000" }},
		{"invalid host while disabled", func(s *LangfuseSetting) { s.Enabled = false; s.Host = "ftp://x" }},
		{"credential header authorization", func(s *LangfuseSetting) {
			s.SessionHeaderNames = []string{"authorization"}
		}},
		{"credential header cookie mixed case", func(s *LangfuseSetting) {
			s.SessionHeaderNames = []string{"CoOkIe"}
		}},
		{"credential header proxy authorization", func(s *LangfuseSetting) {
			s.SessionHeaderNames = []string{"Proxy-Authorization"}
		}},
		{"header with space", func(s *LangfuseSetting) { s.SessionHeaderNames = []string{"X Session"} }},
		{"header with colon", func(s *LangfuseSetting) { s.SessionHeaderNames = []string{"X-Session:"} }},
		{"header empty", func(s *LangfuseSetting) { s.SessionHeaderNames = []string{""} }},
		{"body path empty", func(s *LangfuseSetting) { s.SessionBodyPaths = []string{""} }},
		{"body path blank", func(s *LangfuseSetting) { s.SessionBodyPaths = []string{"   "} }},
		{"body path padded", func(s *LangfuseSetting) { s.SessionBodyPaths = []string{" chat_id"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validSetting()
			tc.mutate(&s)
			assert.Error(t, Validate(s))
		})
	}
}

func TestBuildSnapshot(t *testing.T) {
	s := validSetting()
	s.SessionHeaderNames = []string{"X-Conversation-Id"}
	s.SessionBodyPaths = []string{"metadata.session_id"}

	snapshot, err := BuildSnapshot(s, 7)
	require.NoError(t, err)

	assert.Equal(t, uint64(7), snapshot.Version)
	assert.Equal(t, "http", snapshot.Scheme)
	assert.Equal(t, "langfuse:3000", snapshot.Authority)
	assert.Equal(t, "", snapshot.BasePath)
	assert.Equal(t, "http://langfuse:3000/api/public/otel/v1/traces", snapshot.TracesURL)
	assert.True(t, snapshot.Enabled)

	// Slices must be deep copied so later mutation of the caller's setting can
	// never be observed through the published immutable snapshot.
	s.SessionHeaderNames[0] = "X-Mutated"
	assert.Equal(t, []string{"X-Conversation-Id"}, snapshot.SessionHeaderNames)
}

func TestBuildSnapshotDisabledWithoutHost(t *testing.T) {
	snapshot, err := BuildSnapshot(DefaultLangfuseSetting, 0)
	require.NoError(t, err)

	assert.False(t, snapshot.Enabled)
	assert.Equal(t, "", snapshot.TracesURL)
	assert.Equal(t, "", snapshot.Scheme)
	assert.NotNil(t, snapshot.SessionHeaderNames)
	assert.Empty(t, snapshot.SessionHeaderNames)
	assert.NotNil(t, snapshot.SessionBodyPaths)
	assert.Empty(t, snapshot.SessionBodyPaths)
}

func TestBuildSnapshotRejectsInvalidSetting(t *testing.T) {
	s := validSetting()
	s.SecretKey = ""

	_, err := BuildSnapshot(s, 1)
	assert.Error(t, err)
}

func TestSnapshotEqualConfigIgnoresVersion(t *testing.T) {
	s := validSetting()
	s.SessionHeaderNames = []string{"X-Conversation-Id"}

	a, err := BuildSnapshot(s, 1)
	require.NoError(t, err)
	b, err := BuildSnapshot(s, 99)
	require.NoError(t, err)
	assert.True(t, a.EqualConfig(b))

	s.SessionHeaderNames = []string{"X-Other-Id"}
	c, err := BuildSnapshot(s, 1)
	require.NoError(t, err)
	assert.False(t, a.EqualConfig(c))

	s.SessionHeaderNames = []string{"X-Conversation-Id"}
	s.SampleRate = 0.5
	d, err := BuildSnapshot(s, 1)
	require.NoError(t, err)
	assert.False(t, a.EqualConfig(d))
}
