package langfuse

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

// Structured markers from design §9.1. They are part of the exported payload,
// so their spelling is a contract with the Langfuse UI, not an implementation
// detail.
const (
	markerTruncated     = "_langfuse_truncated"
	markerOmittedBytes  = "_langfuse_omitted_bytes"
	markerOriginalBytes = "_langfuse_original_bytes"
	markerRedacted      = "_langfuse_redacted"
	omissionPrefix      = "_langfuse_omitted:"
)

// maxReductionRounds bounds the shrink loop. Reaching it means the structure
// resists reduction, and the envelope is used instead of looping forever.
const maxReductionRounds = 32

// minStringBudget is the point where shrinking a string leaf further stops
// paying for itself and the whole leaf becomes the omission marker.
const minStringBudget = 8

// dataURLPattern matches the inline binary payloads design §9.2 requires to be
// replaced: only the media type and an estimated size may survive.
var dataURLPattern = regexp.MustCompile(`^data:([^;,]+);base64,`)

// inlineDataContainers are the known object shapes that carry bare base64 in a
// "data" member instead of a data URL.
var inlineDataContainers = map[string]bool{
	"input_audio": true,
	"inline_data": true,
	"inlineData":  true,
	"source":      true,
}

// protectedFields survive field level reduction: they carry the shape a reader
// needs to interpret whatever is left.
var protectedFields = map[string]bool{
	"role":    true,
	"content": true,
	"type":    true,
}

// SanitizeJSON parses a complete JSON document, replaces inline binary payloads
// and then reduces it in the structure layer until its contribution as an OTel
// string attribute fits limit. It returns (nil, false) when the input is not
// parsable JSON; the caller then decides between the raw text fallback and the
// truncation envelope (design §9.2).
func SanitizeJSON(raw []byte, limit int) (sanitized []byte, ok bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			warnCapture("sanitize_panic")
			sanitized, ok = nil, false
		}
	}()

	var value any
	if err := common.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	value = redactValue(value, "")

	encoded, err := common.Marshal(value)
	if err != nil {
		return nil, false
	}
	if jsonEscapedLen(string(encoded)) <= limit {
		return encoded, true
	}
	return reduceToLimit(value, len(raw), limit), true
}

// SanitizeRawText truncates content that was never JSON or SSE. Only this
// fallback may cut text directly, and it still guarantees UTF-8 completeness
// and reports how many bytes it dropped.
func SanitizeRawText(raw []byte, limit int) []byte {
	text := string(raw)
	if jsonEscapedLen(text) <= limit {
		return raw
	}

	// The marker for the largest possible omission reserves the most digits, so
	// the real marker computed after the cut can only be shorter.
	reserved := omissionMarker(len(text))
	low, high := 0, len(text)
	for low < high {
		mid := (low + high + 1) / 2
		for mid > low && mid < len(text) && !utf8.RuneStart(text[mid]) {
			mid--
		}
		if mid == low {
			break
		}
		if jsonEscapedLen(text[:mid]+reserved) <= limit {
			low = mid
		} else {
			high = mid - 1
		}
	}

	prefix := trimIncompleteRune(text[:low])
	return []byte(prefix + omissionMarker(len(text)-len(prefix)))
}

// TruncationEnvelope is the minimal legal document exported when nothing of the
// original content can be preserved: a broken JSON prefix must never be passed
// off as the original request or response.
func TruncationEnvelope(originalBytes int) []byte {
	envelope, err := common.Marshal(map[string]any{
		markerTruncated:     true,
		markerOriginalBytes: originalBytes,
	})
	if err != nil {
		return []byte(fmt.Sprintf(`{"%s":true}`, markerTruncated))
	}
	return envelope
}

// RedactSSEPayload sanitizes one SSE data payload. It tolerates the "data:"
// prefix so callers can hand over a raw line, and returns the input unchanged
// with false when the payload is not JSON (for example "[DONE]").
func RedactSSEPayload(payload []byte) ([]byte, bool) {
	trimmed := strings.TrimSpace(string(payload))
	if rest, found := strings.CutPrefix(trimmed, "data:"); found {
		trimmed = strings.TrimSpace(rest)
	}

	var value any
	if err := common.Unmarshal([]byte(trimmed), &value); err != nil {
		return payload, false
	}
	redacted, err := common.Marshal(redactValue(value, ""))
	if err != nil {
		return payload, false
	}
	return redacted, true
}

// jsonEscapedLen reports how many bytes a string contributes once it is encoded
// as a JSON string attribute, quotes and escapes included. Every size decision
// uses this measure, because Langfuse sizes spans on their JSON representation.
func jsonEscapedLen(s string) int {
	encoded, err := common.Marshal(s)
	if err != nil {
		return len(s) + 2
	}
	return len(encoded)
}

// redactValue walks the decoded document and replaces inline binary payloads.
// parentKey is the key the value was reached through, which is what identifies
// a bare base64 "data" member inside a known inline container.
func redactValue(value any, parentKey string) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			typed[key] = redactValue(child, key)
		}
		if inlineDataContainers[parentKey] {
			if data, isString := typed["data"].(string); isString && data != "" {
				typed["data"] = redactionPlaceholder(inlineMediaType(typed), len(data))
			}
		}
		return typed
	case []any:
		for i, child := range typed {
			typed[i] = redactValue(child, parentKey)
		}
		return typed
	case string:
		if match := dataURLPattern.FindStringSubmatch(typed); match != nil {
			return redactionPlaceholder(match[1], len(typed))
		}
		return typed
	}
	return value
}

func redactionPlaceholder(mediaType string, approxBytes int) map[string]any {
	placeholder := map[string]any{markerRedacted: true, "approx_bytes": approxBytes}
	if mediaType != "" {
		placeholder["media_type"] = mediaType
	}
	return placeholder
}

// inlineMediaType reads the media type an inline container declares next to its
// data member. Nothing is guessed from the payload itself.
func inlineMediaType(container map[string]any) string {
	for _, key := range []string{"media_type", "mime_type", "mimeType"} {
		if declared, isString := container[key].(string); isString && declared != "" {
			return declared
		}
	}
	if format, isString := container["format"].(string); isString && format != "" {
		return "audio/" + format
	}
	return ""
}

// reduceToLimit applies the fixed reduction order of design §9.1: shrink long
// string leaves, then drop trailing array elements, then drop low priority
// object fields, and fall back to the envelope when the structure cannot be
// made to fit.
func reduceToLimit(value any, originalBytes, limit int) []byte {
	if !isContainer(value) {
		// A top level scalar has no structure to reduce.
		return TruncationEnvelope(originalBytes)
	}

	// Strings are shrunk in two passes: first everything that alone eats a
	// quarter of the budget, then whatever is left keeps only its marker.
	stringBudgets := []int{limit / 4, minStringBudget}

	omitted := 0
	for round := 0; round < maxReductionRounds; round++ {
		saved := 0
		if round < len(stringBudgets) {
			value = shrinkLongStrings(value, stringBudgets[round], &saved)
		}
		if saved == 0 {
			saved = dropLargestArrayTail(&value)
		}
		if saved == 0 {
			saved = dropLowestPriorityField(value)
		}
		if saved == 0 {
			break
		}
		omitted += saved

		encoded, err := common.Marshal(withTruncationMarkers(value, omitted))
		if err != nil {
			break
		}
		if jsonEscapedLen(string(encoded)) <= limit {
			return encoded
		}
	}
	return TruncationEnvelope(originalBytes)
}

// withTruncationMarkers returns the value carrying the truncation markers,
// without mutating it: the markers must not be visible to the reduction steps,
// which would otherwise drop or shrink their own bookkeeping.
func withTruncationMarkers(value any, omitted int) any {
	switch typed := value.(type) {
	case map[string]any:
		marked := make(map[string]any, len(typed)+2)
		for key, child := range typed {
			marked[key] = child
		}
		marked[markerTruncated] = true
		marked[markerOmittedBytes] = omitted
		return marked
	case []any:
		return append(slices.Clone(typed), map[string]any{
			markerTruncated:    true,
			markerOmittedBytes: omitted,
		})
	}
	return value
}

func isContainer(value any) bool {
	switch value.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}

// shrinkLongStrings replaces every string leaf longer than budget with a UTF-8
// safe prefix plus the omission marker, accumulating the bytes it dropped.
func shrinkLongStrings(value any, budget int, saved *int) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			typed[key] = shrinkLongStrings(child, budget, saved)
		}
		return typed
	case []any:
		for i, child := range typed {
			typed[i] = shrinkLongStrings(child, budget, saved)
		}
		return typed
	case string:
		if len(typed) <= budget {
			return typed
		}
		shrunk := shrinkText(typed, budget)
		if len(shrunk) >= len(typed) {
			return typed
		}
		*saved += len(typed) - len(shrunk)
		return shrunk
	}
	return value
}

// shrinkText cuts a string leaf down to budget bytes on a rune boundary and
// appends the marker naming how many bytes were dropped.
func shrinkText(text string, budget int) string {
	keep := budget - len(omissionMarker(len(text)))
	if keep < 0 {
		keep = 0
	}
	if keep > len(text) {
		keep = len(text)
	}
	for keep > 0 && keep < len(text) && !utf8.RuneStart(text[keep]) {
		keep--
	}
	prefix := trimIncompleteRune(text[:keep])
	return prefix + omissionMarker(len(text)-len(prefix))
}

// dropLargestArrayTail removes trailing elements of the largest array in the
// document, which is where §9.1 wants message and content blocks to go first.
// A quarter of the tail goes per round so a long conversation still converges
// inside the round budget. It returns the bytes those elements contributed.
func dropLargestArrayTail(root *any) int {
	best := arrayLocation{}
	visitArrays(*root, nil, "", &best)
	if len(best.elements) == 0 {
		return 0
	}

	keep := len(best.elements) - max(1, len(best.elements)/4)
	dropped, err := common.Marshal(best.elements[keep:])
	if err != nil {
		return 0
	}
	trimmed := best.elements[:keep]
	switch container := best.holder.(type) {
	case map[string]any:
		container[best.key] = trimmed
	case []any:
		index, err := strconv.Atoi(best.key)
		if err != nil {
			return 0
		}
		container[index] = trimmed
	default:
		*root = trimmed // the document root is the array itself
	}
	return len(dropped)
}

// arrayLocation is the array a reduction round targets together with whatever
// holds it, so the shortened slice can be written back.
type arrayLocation struct {
	holder   any
	key      string
	elements []any
}

func visitArrays(value, holder any, key string, best *arrayLocation) {
	switch typed := value.(type) {
	case map[string]any:
		for _, childKey := range sortedKeys(typed) {
			visitArrays(typed[childKey], typed, childKey, best)
		}
	case []any:
		if len(typed) > len(best.elements) {
			best.holder, best.key, best.elements = holder, key, typed
		}
		for i, child := range typed {
			visitArrays(child, typed, strconv.Itoa(i), best)
		}
	}
}

// dropLowestPriorityField removes the lexicographically last unprotected field
// of the object that carries the most droppable fields.
func dropLowestPriorityField(value any) int {
	var target map[string]any
	var targetKey string
	best := -1

	visitObjects(value, func(object map[string]any) {
		droppable := 0
		lastKey := ""
		for _, key := range sortedKeys(object) {
			if protectedFields[key] || strings.HasPrefix(key, "_langfuse_") {
				continue
			}
			droppable++
			lastKey = key
		}
		if droppable > best {
			best, target, targetKey = droppable, object, lastKey
		}
	})
	if target == nil || targetKey == "" {
		return 0
	}

	dropped, err := common.Marshal(target[targetKey])
	if err != nil {
		return 0
	}
	delete(target, targetKey)
	return len(dropped) + jsonEscapedLen(targetKey)
}

func visitObjects(value any, visit func(map[string]any)) {
	switch typed := value.(type) {
	case map[string]any:
		visit(typed)
		for _, key := range sortedKeys(typed) {
			visitObjects(typed[key], visit)
		}
	case []any:
		for _, child := range typed {
			visitObjects(child, visit)
		}
	}
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func omissionMarker(bytes int) string {
	return fmt.Sprintf("%s%d bytes_", omissionPrefix, bytes)
}

// trimIncompleteRune drops a trailing byte sequence that no longer forms a
// complete rune, which keeps every truncation valid UTF-8.
func trimIncompleteRune(text string) string {
	for len(text) > 0 {
		r, size := utf8.DecodeLastRuneInString(text)
		if r != utf8.RuneError || size > 1 {
			break
		}
		text = text[:len(text)-1]
	}
	return text
}
