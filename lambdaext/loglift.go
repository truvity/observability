package lambdaext

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Bounds on what one log line may cost. A function controls its log lines,
// so none of them is trusted to be small.
const (
	// maxLiftedAttrs bounds the attributes one log line contributes.
	maxLiftedAttrs = 64
	// maxLineBytes is the longest line that is parsed as JSON and the longest
	// body kept; beyond it the line is forwarded as truncated text.
	maxLineBytes = 64 << 10
	// maxKeyLen and maxValueLen bound an attribute key and a string (or
	// nested JSON text) value; a longer value is cut and marked.
	maxKeyLen   = 128
	maxValueLen = 4 << 10
	// truncatedMark ends a value that was cut.
	truncatedMark = "...[truncated]"
	// How far the record time a line names may be from the time it was
	// observed: a line older than a week or later than five minutes is not a
	// clock to believe.
	maxLineAge   = 7 * 24 * time.Hour
	maxLineAhead = 5 * time.Minute
)

// reservedAttr is the attribute namespaces the extension owns: the Lambda
// resource and the event's identity. A line's key in them is kept, renamed
// with the attrPrefix, so a line cannot impersonate the platform.
var reservedAttr = []string{"service.", "cloud.", "faas.", "k8s.", "host.", "telemetry.", "otel.", "log."}

const attrPrefix = "log.attr."

func reserved(key string) bool {
	if key == "event.name" {
		return true
	}
	for _, p := range reservedAttr {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// cut shortens s to at most n bytes on a rune boundary, and reports whether
// it did. The result is valid UTF-8 (protobuf strings must be).
func cut(s string, n int) (string, bool) {
	short := len(s) > n
	if short {
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = s[:n]
	}
	return strings.ToValidUTF8(s, "\uFFFD"), short
}

// capValue is cut with the marker appended.
func capValue(s string, n int) string {
	v, cutShort := cut(s, n)
	if cutShort {
		return v + truncatedMark
	}
	return v
}

// field is one top-level member of a JSON object, in line order.
type field struct {
	key string
	val any
}

// decodeObject parses raw as a single JSON object, numbers kept exact and
// members in line order (a repeated key keeps its first place and its last
// value). ok is false for anything else (text, an array, a scalar, trailing
// data) and for a line over maxLineBytes.
func decodeObject(raw []byte) ([]field, bool) {
	if len(raw) > maxLineBytes {
		return nil, false
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}
	var out []field
	index := map[string]int{}
	for dec.More() {
		tok, err := dec.Token()
		key, isStr := tok.(string)
		if err != nil || !isStr {
			return nil, false
		}
		var v any
		if err = dec.Decode(&v); err != nil {
			return nil, false
		}
		if i, dup := index[key]; dup {
			out[i].val = v
			continue
		}
		index[key] = len(out)
		out = append(out, field{key, v})
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return out, true
}

// take returns the first of keys (in that order) whose member is a string
// that ok accepts, and the string.
func take(obj []field, used map[string]bool, keys []string, ok func(string) bool) (string, string, bool) {
	for _, k := range keys {
		for _, f := range obj {
			if f.key != k || used[k] {
				continue
			}
			if s, isStr := f.val.(string); isStr && ok(s) {
				return k, s, true
			}
		}
	}
	return "", "", false
}

var (
	levelKeys = []string{"level", "severity", "Level"}
	msgKeys   = []string{"msg", "message", "Message"}
	timeKeys  = []string{"time", "timestamp", "ts"}
)

// liftJSON maps a function log line that is a JSON object onto rec. rec's
// observed time is the clock lifted timestamps are checked against.
//
//   - trace_id (32 hex) and span_id (16 hex) become the record's trace
//     context, when valid and not all zero; trace_flags (2 hex) sets the flags.
//     The line is trusted for them: see the docs.
//   - level (else severity, Level) sets the severity: a word (slog's offset
//     forms such as ERROR+2 by their prefix) or a pino number (10..60).
//   - msg (else message, Message) is the body.
//   - time (else timestamp, ts), RFC 3339, is the record time when it is after
//     1970 and within maxLineAge before, maxLineAhead after, the observed
//     time; otherwise the event's time stays and the text is log.time.
//   - requestId is kept as log.attr.requestId (the extension sets requestId and
//     faas.invocation_id itself, from the invocation, never from a line).
//   - every other key is an attribute, in line order, at most maxLiftedAttrs;
//     a key in a namespace the extension owns is renamed log.attr.<key>, a
//     long key or value is cut, nested values are JSON text, null is dropped.
//
// A key whose value has the wrong shape (a level that is no known severity, a
// trace_id that is not hex) is not consumed: it stays an attribute.
func liftJSON(rec *LogRecord, obj []field) {
	used := map[string]bool{}
	get := func(key string) any {
		for _, f := range obj {
			if f.key == key {
				return f.val
			}
		}
		return nil
	}
	if tid, ok := hexField(get("trace_id"), 16, true); ok {
		rec.TraceID = tid
		used["trace_id"] = true
		if sid, ok := hexField(get("span_id"), 8, true); ok {
			rec.SpanID = sid
			used["span_id"] = true
		}
		if f, ok := hexField(get("trace_flags"), 1, false); ok {
			rec.Flags = uint32(f[0])
			used["trace_flags"] = true
		}
	}
	if k, lv, ok := take(obj, used, levelKeys, func(s string) bool { _, known := severityOf(s); return known }); ok {
		rec.Severity, _ = severityOf(lv)
		used[k] = true
	} else if k, n, ok := pinoLevel(obj, used); ok {
		rec.Severity = n
		used[k] = true
	}
	if k, m, ok := take(obj, used, msgKeys, func(string) bool { return true }); ok {
		rec.Body, _ = cut(m, maxLineBytes)
		used[k] = true
	}
	if k, ts, ok := take(obj, used, timeKeys, func(s string) bool { _, err := time.Parse(time.RFC3339Nano, s); return err == nil }); ok {
		used[k] = true
		t, _ := time.Parse(time.RFC3339Nano, ts)
		observed := time.Unix(0, int64(rec.ObservedTimeUnixNano)) //nolint:gosec // a post-1970 time
		if t.Unix() > 0 && !t.Before(observed.Add(-maxLineAge)) && !t.After(observed.Add(maxLineAhead)) {
			rec.TimeUnixNano = uint64(t.UnixNano()) //nolint:gosec // inside the window just checked
		} else {
			rec.Attrs = append(rec.Attrs, strAttr("log.time", capValue(ts, maxKeyLen)))
		}
	}
	n := 0
	for _, f := range obj {
		if used[f.key] {
			continue
		}
		if n >= maxLiftedAttrs {
			rec.Attrs = append(rec.Attrs, strAttr("log.attrs_truncated", "true"))
			break
		}
		key := f.key
		if reserved(key) || key == "requestId" {
			key = attrPrefix + key
		}
		key, _ = cut(key, maxKeyLen)
		if a, ok := anyAttr(key, f.val); ok {
			rec.Attrs = append(rec.Attrs, a)
			n++
		}
	}
}

// hexField decodes v as n bytes of hex; when nonZero, an all-zero value is
// invalid (W3C trace context), as it is not for trace_flags.
func hexField(v any, n int, nonZero bool) ([]byte, bool) {
	s, ok := v.(string)
	if !ok || len(s) != 2*n {
		return nil, false
	}
	b, err := hex.DecodeString(strings.ToLower(s))
	if err != nil || (nonZero && bytes.Equal(b, make([]byte, n))) {
		return nil, false
	}
	return b, true
}

// severityOf maps a level word to a severity. slog's offset forms (ERROR+2,
// DEBUG-4) are recognised by the word before the offset.
func severityOf(level string) (int32, bool) {
	word := strings.ToUpper(strings.TrimSpace(level))
	if i := strings.IndexAny(word, "+-"); i > 0 {
		if _, err := strconv.Atoi(word[i:]); err == nil {
			word = word[:i]
		}
	}
	switch word {
	case "TRACE", "DEBUG":
		return SeverityDebug, true
	case "INFO", "NOTICE":
		return SeverityInfo, true
	case "WARN", "WARNING":
		return SeverityWarn, true
	case "ERROR", "ERR":
		return SeverityError, true
	case "FATAL", "CRITICAL", "PANIC":
		return SeverityFatal, true
	}
	return 0, false
}

// pinoLevel reads the numeric levels pino uses: 10 trace, 20 debug, 30 info,
// 40 warn, 50 error, 60 fatal.
func pinoLevel(obj []field, used map[string]bool) (string, int32, bool) {
	for _, k := range levelKeys {
		for _, f := range obj {
			num, isNum := f.val.(json.Number)
			if f.key != k || used[k] || !isNum {
				continue
			}
			switch num.String() {
			case "10", "20":
				return k, SeverityDebug, true
			case "30":
				return k, SeverityInfo, true
			case "40":
				return k, SeverityWarn, true
			case "50":
				return k, SeverityError, true
			case "60":
				return k, SeverityFatal, true
			}
		}
	}
	return "", 0, false
}

func anyAttr(k string, v any) (Attr, bool) {
	switch x := v.(type) {
	case nil:
		return Attr{}, false
	case string:
		return strAttr(k, capValue(x, maxValueLen)), true
	case bool:
		return strAttr(k, strconv.FormatBool(x)), true
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return intAttr(k, n), true
		}
		if f, err := x.Float64(); err == nil && !math.IsInf(f, 0) {
			return floatAttr(k, f), true
		}
		return strAttr(k, capValue(x.String(), maxKeyLen)), true // beyond int64 and float64: the digits
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return Attr{}, false
		}
		return strAttr(k, capValue(string(b), maxValueLen)), true
	}
}

// invocationTrace remembers the X-Ray trace context of the invocation the
// platform last announced, so a function log line that carries no trace of its
// own (a plain-text line, or JSON without trace_id) still lines up with the
// invocation's trace. It is the fallback; a lifted trace_id always wins.
type invocationTrace struct {
	mu        sync.Mutex
	requestID string
	traceID   []byte
	spanID    []byte
	flags     uint32
}

// correlate keeps the invocation's trace from platform events and applies it
// to function records. A function line gets requestId and faas.invocation_id
// from the invocation in flight, never from the line, and only when the line
// names no request or names that one; it takes the invocation's trace when it
// has none of its own.
func (t *Telemetry) correlate(ev TelemetryEvent, rec *LogRecord) {
	iv := &t.inv
	iv.mu.Lock()
	defer iv.mu.Unlock()
	switch ev.Type {
	case "platform.start":
		iv.requestID, iv.traceID, iv.spanID, iv.flags = "", nil, nil, 0
		var p platformRecord
		if json.Unmarshal(ev.Record, &p) == nil {
			iv.requestID = p.RequestID
			iv.traceID, iv.spanID, iv.flags = traceContext(&p)
		}
	case "platform.runtimeDone", "platform.report":
		iv.requestID, iv.traceID, iv.spanID, iv.flags = "", nil, nil, 0
	case "function":
		if iv.requestID == "" {
			return
		}
		for _, a := range rec.Attrs {
			if a.Key == attrPrefix+"requestId" && a.Str != iv.requestID {
				return // a line naming another invocation: no guess
			}
		}
		rec.Attrs = append(rec.Attrs, strAttr("requestId", iv.requestID), strAttr("faas.invocation_id", iv.requestID))
		if len(rec.TraceID) == 0 && len(iv.traceID) > 0 {
			rec.TraceID, rec.SpanID, rec.Flags = iv.traceID, iv.spanID, iv.flags
		}
	}
}
