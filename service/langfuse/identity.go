package langfuse

import (
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// LangfuseSessionHeader is the only session header read without configuration.
// X-Session-Id has no consistent meaning across deployments, so it is a session
// source only once an administrator lists it explicitly (design §7.2).
const LangfuseSessionHeader = "X-Langfuse-Session-Id"

// maxSessionIDBytes is the hard length limit of a raw session ID. Oversized
// values are rejected rather than truncated, because truncation would merge
// distinct client IDs into one Langfuse session.
const maxSessionIDBytes = 200

// Closed session omission enums from design §7.2. They are a stable telemetry
// schema: no call site may concatenate, derive or extend these values.
const (
	SessionOmittedEmptyAfterTrim       = "empty_after_trim"
	SessionOmittedInvalidUTF8          = "invalid_utf8"
	SessionOmittedInvalidControlChar   = "invalid_control_character"
	SessionOmittedTooLong              = "too_long"
	SessionOmittedUserScopeUnavailable = "user_scope_unavailable"

	SessionBodyOmittedNonJSONContentType  = "non_json_content_type"
	SessionBodyOmittedStorageUnavailable  = "body_storage_unavailable"
	SessionBodyOmittedStorageTypeMismatch = "body_storage_type_mismatch"
	SessionBodyOmittedSizeUnknown         = "body_size_unknown"
	SessionBodyOmittedTooLarge            = "body_too_large"
	SessionBodyOmittedReadFailed          = "body_read_failed"
	SessionBodyOmittedReadIncomplete      = "body_read_incomplete"
	SessionBodyOmittedInvalidJSON         = "invalid_json"
	SessionBodyOmittedNoSupportedScalar   = "no_supported_scalar"
)

// SessionIdentity is the result of one explicit session extraction round. The
// raw value stays in memory for scoping and diagnostics only: metadata carries
// the source and the omission reasons, never the client supplied value.
type SessionIdentity struct {
	// ScopedID is "{userId}:{raw}" and is filled in by the Recorder once the
	// user scope is known; it is empty without a legal session or a positive
	// user ID.
	ScopedID string
	// RawSessionID is the trimmed, validated client value. It must never be
	// exported as an attribute.
	RawSessionID string
	// Source is the lower-cased header name or the gjson path the value was
	// locked to. It is set even when the locked candidate was rejected, so the
	// omission stays diagnosable; empty means no candidate was provided at all.
	Source string

	OmittedReason     string
	BodyOmittedReason string
}

// extractHeaderSession applies the fixed header priority of design §7.2: the
// standard header first, then the configured names in order. The first raw
// non-empty value locks the source, and a rejected lock never falls back to a
// lower priority source. The getter is injected so the caller decides whether
// the values come from a request or a test fixture.
func extractHeaderSession(header func(name string) string, snap Snapshot) SessionIdentity {
	names := make([]string, 0, len(snap.SessionHeaderNames)+1)
	names = append(names, LangfuseSessionHeader)
	names = append(names, snap.SessionHeaderNames...)

	for _, name := range names {
		raw := header(name)
		if raw == "" {
			continue
		}
		// Locking happens before trimming so a whitespace-only value is a
		// deliberate rejection rather than a silent fallback.
		source := strings.ToLower(name)
		value := strings.TrimSpace(raw)
		if reason := rejectSessionValue(value); reason != "" {
			return SessionIdentity{Source: source, OmittedReason: reason}
		}
		return SessionIdentity{RawSessionID: value, Source: source}
	}
	return SessionIdentity{}
}

// extractBodySession queries the configured gjson paths against the cached
// request body. It is only reached when no header carried a raw non-empty
// candidate. The returned buffer is the complete body that was read, so a
// retirement retry can re-query new paths without a second read; it is nil
// whenever no complete body is available. This path never calls
// common.GetBodyStorage or common.GetRequestBody: telemetry must not become the
// first consumer of the request body (design §9.1).
func extractBodySession(c *gin.Context, snap Snapshot, reuse []byte) (SessionIdentity, []byte) {
	if len(snap.SessionBodyPaths) == 0 {
		return SessionIdentity{}, reuse
	}

	body := reuse
	if body == nil {
		var reason string
		if body, reason = readSessionBody(c, snap); reason != "" {
			return SessionIdentity{BodyOmittedReason: reason}, nil
		}
	}

	if !gjson.ValidBytes(body) {
		return SessionIdentity{BodyOmittedReason: SessionBodyOmittedInvalidJSON}, body
	}
	for _, path := range snap.SessionBodyPaths {
		result := gjson.GetBytes(body, path)
		if !result.Exists() {
			continue
		}
		var value string
		switch result.Type {
		case gjson.String:
			value = strings.TrimSpace(result.String())
		case gjson.Number:
			// A plain decimal keeps the ID stable: exponent notation would make
			// the same upstream number produce two different session IDs.
			value = strconv.FormatFloat(result.Float(), 'f', -1, 64)
		default:
			// bool, null, array and object are ignored rather than serialized.
			continue
		}
		if value == "" {
			continue
		}
		if reason := rejectSessionValue(value); reason != "" {
			return SessionIdentity{Source: path, OmittedReason: reason}, body
		}
		return SessionIdentity{RawSessionID: value, Source: path}, body
	}
	return SessionIdentity{BodyOmittedReason: SessionBodyOmittedNoSupportedScalar}, body
}

// readSessionBody reads the complete cached body when it fits the dedicated
// session limit. The failure conditions are returned in the fixed order of
// design §7.2, so the same request always reports the same reason.
func readSessionBody(c *gin.Context, snap Snapshot) ([]byte, string) {
	if !isJSONContentType(c.Request.Header.Get("Content-Type")) {
		return nil, SessionBodyOmittedNonJSONContentType
	}
	cached, exists := c.Get(common.KeyBodyStorage)
	if !exists || cached == nil {
		return nil, SessionBodyOmittedStorageUnavailable
	}
	storage, ok := cached.(common.BodyStorage)
	if !ok {
		return nil, SessionBodyOmittedStorageTypeMismatch
	}
	size := storage.Size()
	if size < 0 {
		return nil, SessionBodyOmittedSizeUnknown
	}
	limit := int64(snap.MaxSessionBodyBytes)
	if size > limit {
		return nil, SessionBodyOmittedTooLarge
	}

	reader, err := storage.NewReader()
	if err != nil {
		return nil, SessionBodyOmittedReadFailed
	}
	defer reader.Close()
	// Reading one byte past the limit catches a storage that under-reports its
	// size; gjson needs the whole document, so a prefix is never usable here.
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, SessionBodyOmittedReadFailed
	}
	if int64(len(body)) > limit {
		return nil, SessionBodyOmittedTooLarge
	}
	if int64(len(body)) != size {
		return nil, SessionBodyOmittedReadIncomplete
	}
	return body, ""
}

func isJSONContentType(contentType string) bool {
	mediaType, _, _ := strings.Cut(contentType, ";")
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

// rejectSessionValue returns the closed omission reason for an illegal session
// value, or "" when the trimmed value may be used as-is.
func rejectSessionValue(value string) string {
	if value == "" {
		return SessionOmittedEmptyAfterTrim
	}
	if !utf8.ValidString(value) {
		return SessionOmittedInvalidUTF8
	}
	if strings.ContainsFunc(value, unicode.IsControl) {
		return SessionOmittedInvalidControlChar
	}
	if len(value) > maxSessionIDBytes {
		return SessionOmittedTooLong
	}
	return ""
}

// scopeSession puts a raw session into the tenant namespace of design §7.2.
// Langfuse sessions are global inside a project, so a client controlled value
// may only be exported with the numeric user ID prefix; without a positive user
// ID the session is omitted instead of degrading to a project wide raw value.
func scopeSession(userID int, raw string) (string, bool) {
	if userID <= 0 || raw == "" {
		return "", false
	}
	return strconv.Itoa(userID) + ":" + raw, true
}
