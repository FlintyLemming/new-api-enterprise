package langfuse

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/langfuse_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sessionSnapshot(headerNames, bodyPaths []string, maxSessionBodyBytes int) Snapshot {
	setting := langfuse_setting.DefaultLangfuseSetting
	setting.SessionHeaderNames = headerNames
	setting.SessionBodyPaths = bodyPaths
	if maxSessionBodyBytes > 0 {
		setting.MaxSessionBodyBytes = maxSessionBodyBytes
	}
	return Snapshot{LangfuseSetting: setting}
}

func headerGetter(pairs map[string]string) func(string) string {
	header := http.Header{}
	for name, value := range pairs {
		header.Set(name, value)
	}
	return header.Get
}

func TestSessionHeaderExtraction(t *testing.T) {
	longValue := strings.Repeat("a", 201)

	cases := []struct {
		name        string
		headers     map[string]string
		headerNames []string
		bodyPaths   []string
		want        SessionIdentity
	}{
		{
			name:        "standard header wins and is trimmed",
			headers:     map[string]string{LangfuseSessionHeader: " abc ", "X-Session-Id": "generic"},
			headerNames: []string{"X-Session-Id"},
			want:        SessionIdentity{RawSessionID: "abc", Source: "x-langfuse-session-id"},
		},
		{
			name:    "generic session header is not read unless configured",
			headers: map[string]string{"X-Session-Id": "generic"},
			want:    SessionIdentity{},
		},
		{
			name:        "generic session header is read once configured",
			headers:     map[string]string{"X-Session-Id": "generic"},
			headerNames: []string{"X-Session-Id"},
			want:        SessionIdentity{RawSessionID: "generic", Source: "x-session-id"},
		},
		{
			name:        "locked standard header does not fall back to configured header",
			headers:     map[string]string{LangfuseSessionHeader: longValue, "X-Session-Id": "generic"},
			headerNames: []string{"X-Session-Id"},
			bodyPaths:   []string{"session_id"},
			want:        SessionIdentity{Source: "x-langfuse-session-id", OmittedReason: SessionOmittedTooLong},
		},
		{
			name:        "configured headers are tried in order",
			headers:     map[string]string{"X-Alpha": "alpha", "X-Beta": "beta"},
			headerNames: []string{"X-Beta", "X-Alpha"},
			want:        SessionIdentity{RawSessionID: "beta", Source: "x-beta"},
		},
		{
			name:        "blank header value locks the source and is rejected",
			headers:     map[string]string{LangfuseSessionHeader: "   ", "X-Alpha": "alpha"},
			headerNames: []string{"X-Alpha"},
			want:        SessionIdentity{Source: "x-langfuse-session-id", OmittedReason: SessionOmittedEmptyAfterTrim},
		},
		{
			name:    "carriage return is rejected",
			headers: map[string]string{LangfuseSessionHeader: "ab\rcd"},
			want:    SessionIdentity{Source: "x-langfuse-session-id", OmittedReason: SessionOmittedInvalidControlChar},
		},
		{
			name:    "line feed is rejected",
			headers: map[string]string{LangfuseSessionHeader: "ab\ncd"},
			want:    SessionIdentity{Source: "x-langfuse-session-id", OmittedReason: SessionOmittedInvalidControlChar},
		},
		{
			name:    "other control characters are rejected",
			headers: map[string]string{LangfuseSessionHeader: "ab\x01cd"},
			want:    SessionIdentity{Source: "x-langfuse-session-id", OmittedReason: SessionOmittedInvalidControlChar},
		},
		{
			name:    "invalid utf8 is rejected",
			headers: map[string]string{LangfuseSessionHeader: "\xff\xfe"},
			want:    SessionIdentity{Source: "x-langfuse-session-id", OmittedReason: SessionOmittedInvalidUTF8},
		},
		{
			name:    "values longer than 200 bytes are rejected, never truncated",
			headers: map[string]string{LangfuseSessionHeader: longValue},
			want:    SessionIdentity{Source: "x-langfuse-session-id", OmittedReason: SessionOmittedTooLong},
		},
		{
			name:    "exactly 200 bytes is accepted",
			headers: map[string]string{LangfuseSessionHeader: strings.Repeat("a", 200)},
			want:    SessionIdentity{RawSessionID: strings.Repeat("a", 200), Source: "x-langfuse-session-id"},
		},
		{
			name:        "no candidate at all writes no reason",
			headers:     map[string]string{"X-Unrelated": "value"},
			headerNames: []string{"X-Alpha"},
			want:        SessionIdentity{},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			snapshot := sessionSnapshot(testCase.headerNames, testCase.bodyPaths, 0)
			assert.Equal(t, testCase.want, extractHeaderSession(headerGetter(testCase.headers), snapshot))
		})
	}
}

// trackingStorage is a common.BodyStorage whose size, payload and reader
// behaviour are controlled per test, so the body path can be driven into every
// closed omission reason without a real request pipeline.
type trackingStorage struct {
	payload     []byte
	size        int64
	readerErr   error
	shortRead   int
	readerCalls int
}

func (s *trackingStorage) Read(p []byte) (int, error) { return 0, io.EOF }
func (s *trackingStorage) Seek(int64, int) (int64, error) {
	return 0, nil
}
func (s *trackingStorage) Close() error           { return nil }
func (s *trackingStorage) Bytes() ([]byte, error) { return s.payload, nil }
func (s *trackingStorage) Size() int64            { return s.size }
func (s *trackingStorage) IsDisk() bool           { return false }

func (s *trackingStorage) NewReader() (io.ReadCloser, error) {
	s.readerCalls++
	if s.readerErr != nil {
		return nil, s.readerErr
	}
	payload := s.payload
	if s.shortRead > 0 && s.shortRead < len(payload) {
		payload = payload[:s.shortRead]
	}
	return io.NopCloser(strings.NewReader(string(payload))), nil
}

func newStorage(payload string) *trackingStorage {
	return &trackingStorage{payload: []byte(payload), size: int64(len(payload))}
}

func bodyContext(t *testing.T, contentType string, storage any) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	c.Request.Header.Set("Content-Type", contentType)
	if storage != nil {
		c.Set(common.KeyBodyStorage, storage)
	}
	return c
}

func TestSessionBodyExtraction(t *testing.T) {
	const jsonCT = "application/json; charset=utf-8"
	paths := []string{"metadata.session_id", "session_id"}

	t.Run("first supported scalar locks the source", func(t *testing.T) {
		storage := newStorage(`{"metadata":{"session_id":" chat-7 "},"session_id":"ignored"}`)
		c := bodyContext(t, jsonCT, storage)
		identity, buf := extractBodySession(c, sessionSnapshot(nil, paths, 0), nil)
		assert.Equal(t, SessionIdentity{RawSessionID: "chat-7", Source: "metadata.session_id"}, identity)
		assert.Equal(t, storage.payload, buf)
		assert.Equal(t, 1, storage.readerCalls)
	})

	t.Run("json numbers are formatted without exponent", func(t *testing.T) {
		c := bodyContext(t, jsonCT, newStorage(`{"session_id":1234567890}`))
		identity, _ := extractBodySession(c, sessionSnapshot(nil, []string{"session_id"}, 0), nil)
		assert.Equal(t, "1234567890", identity.RawSessionID)
		assert.Equal(t, "session_id", identity.Source)
		assert.Empty(t, identity.OmittedReason)
	})

	t.Run("unsupported scalar types and misses are skipped", func(t *testing.T) {
		body := `{"a":true,"b":[1,2],"c":{"x":1},"d":null,"e":""}`
		c := bodyContext(t, jsonCT, newStorage(body))
		snapshot := sessionSnapshot(nil, []string{"a", "b", "c", "d", "e", "missing"}, 0)
		identity, _ := extractBodySession(c, snapshot, nil)
		assert.Equal(t, SessionIdentity{BodyOmittedReason: SessionBodyOmittedNoSupportedScalar}, identity)
	})

	t.Run("scalar that fails validation writes only the session reason", func(t *testing.T) {
		body := `{"session_id":"` + strings.Repeat("a", 201) + `"}`
		c := bodyContext(t, jsonCT, newStorage(body))
		identity, _ := extractBodySession(c, sessionSnapshot(nil, []string{"session_id"}, 0), nil)
		assert.Equal(t, SessionIdentity{Source: "session_id", OmittedReason: SessionOmittedTooLong}, identity)
		assert.Empty(t, identity.BodyOmittedReason)
	})

	t.Run("non json content type is reported before storage access", func(t *testing.T) {
		c := bodyContext(t, "text/plain", nil)
		identity, buf := extractBodySession(c, sessionSnapshot(nil, paths, 0), nil)
		assert.Equal(t, SessionIdentity{BodyOmittedReason: SessionBodyOmittedNonJSONContentType}, identity)
		assert.Nil(t, buf)
	})

	t.Run("missing storage is reported", func(t *testing.T) {
		c := bodyContext(t, jsonCT, nil)
		identity, _ := extractBodySession(c, sessionSnapshot(nil, paths, 0), nil)
		assert.Equal(t, SessionBodyOmittedStorageUnavailable, identity.BodyOmittedReason)
	})

	t.Run("wrong storage type is reported", func(t *testing.T) {
		c := bodyContext(t, jsonCT, "not-a-storage")
		identity, _ := extractBodySession(c, sessionSnapshot(nil, paths, 0), nil)
		assert.Equal(t, SessionBodyOmittedStorageTypeMismatch, identity.BodyOmittedReason)
	})

	t.Run("unknown size is reported", func(t *testing.T) {
		storage := newStorage(`{"session_id":"x"}`)
		storage.size = -1
		c := bodyContext(t, jsonCT, storage)
		identity, _ := extractBodySession(c, sessionSnapshot(nil, paths, 0), nil)
		assert.Equal(t, SessionBodyOmittedSizeUnknown, identity.BodyOmittedReason)
		assert.Zero(t, storage.readerCalls)
	})

	t.Run("oversized body is never read", func(t *testing.T) {
		storage := newStorage(`{"session_id":"` + strings.Repeat("a", 4096) + `"}`)
		c := bodyContext(t, jsonCT, storage)
		identity, buf := extractBodySession(c, sessionSnapshot(nil, paths, 1024), nil)
		assert.Equal(t, SessionBodyOmittedTooLarge, identity.BodyOmittedReason)
		assert.Zero(t, storage.readerCalls, "an oversized body must not be opened")
		assert.Nil(t, buf)
	})

	t.Run("reader failure is reported", func(t *testing.T) {
		storage := newStorage(`{"session_id":"x"}`)
		storage.readerErr = common.ErrStorageClosed
		c := bodyContext(t, jsonCT, storage)
		identity, _ := extractBodySession(c, sessionSnapshot(nil, paths, 0), nil)
		assert.Equal(t, SessionBodyOmittedReadFailed, identity.BodyOmittedReason)
	})

	t.Run("short read is reported", func(t *testing.T) {
		storage := newStorage(`{"session_id":"chat-7"}`)
		storage.shortRead = 5
		c := bodyContext(t, jsonCT, storage)
		identity, buf := extractBodySession(c, sessionSnapshot(nil, paths, 0), nil)
		assert.Equal(t, SessionBodyOmittedReadIncomplete, identity.BodyOmittedReason)
		assert.Nil(t, buf, "an incomplete read must not be reused")
	})

	t.Run("invalid json is reported", func(t *testing.T) {
		c := bodyContext(t, jsonCT, newStorage(`{"session_id":`))
		identity, _ := extractBodySession(c, sessionSnapshot(nil, paths, 0), nil)
		assert.Equal(t, SessionBodyOmittedInvalidJSON, identity.BodyOmittedReason)
	})

	t.Run("reused buffer is re-queried without touching storage", func(t *testing.T) {
		storage := newStorage(`{"metadata":{"session_id":"chat-7"}}`)
		c := bodyContext(t, jsonCT, storage)
		identity, buf := extractBodySession(c, sessionSnapshot(nil, []string{"absent"}, 0), nil)
		require.Equal(t, SessionBodyOmittedNoSupportedScalar, identity.BodyOmittedReason)
		require.NotNil(t, buf)
		require.Equal(t, 1, storage.readerCalls)

		retried, reused := extractBodySession(c, sessionSnapshot(nil, paths, 0), buf)
		assert.Equal(t, SessionIdentity{RawSessionID: "chat-7", Source: "metadata.session_id"}, retried)
		assert.Equal(t, buf, reused)
		assert.Equal(t, 1, storage.readerCalls, "reuse must not open the storage again")
	})
}

func TestSessionScoping(t *testing.T) {
	scoped, ok := scopeSession(42, "default")
	assert.True(t, ok)
	assert.Equal(t, "42:default", scoped)

	otherUser, ok := scopeSession(43, "default")
	require.True(t, ok)
	assert.NotEqual(t, scoped, otherUser, "the same raw session must not collide across users")

	for _, userID := range []int{0, -1} {
		scoped, ok := scopeSession(userID, "x")
		assert.False(t, ok)
		assert.Empty(t, scoped)
	}

	empty, ok := scopeSession(42, "")
	assert.False(t, ok)
	assert.Empty(t, empty)
}
