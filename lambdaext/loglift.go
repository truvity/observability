package lambdaext

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxLiftedAttrs bounds the attributes one log line contributes.
const maxLiftedAttrs = 64

// decodeObject parses raw as a single JSON object, numbers kept exact. ok is
// false for anything else (text, an array, a scalar, trailing data).
func decodeObject(raw []byte) (map[string]any, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil || dec.More() {
		return nil, false
	}
	return obj, true
}

// liftJSON maps a function log line that is a JSON object onto rec:
//
//   - trace_id (32 hex) and span_id (16 hex) become the record's trace
//     context, when valid and not all zero; trace_flags (2 hex) sets the flags.
//   - level sets the severity; msg (else message) is the body; time (else
//     timestamp) is the record time, RFC 3339.
//   - requestId (the Lambda JSON log format) is kept as requestId and
//     faas.invocation_id.
//   - every other key becomes an attribute; nested values are kept as JSON
//     text, null is dropped.
//
// A key whose value has the wrong shape (a numeric level, a trace_id that is
// not hex) is not consumed: it stays an attribute.
func liftJSON(rec *LogRecord, obj map[string]any) {
	consumed := map[string]bool{}
	if tid, ok := hexField(obj, "trace_id", 16); ok {
		rec.TraceID = tid
		consumed["trace_id"] = true
		if sid, ok := hexField(obj, "span_id", 8); ok {
			rec.SpanID = sid
			consumed["span_id"] = true
		}
		if f, ok := hexField(obj, "trace_flags", 1); ok {
			rec.Flags = uint32(f[0])
			consumed["trace_flags"] = true
		}
	}
	if lv, ok := obj["level"].(string); ok {
		if sev, known := severityOf(lv); known {
			rec.Severity = sev
			consumed["level"] = true
		}
	}
	for _, k := range []string{"msg", "message"} {
		if m, ok := obj[k].(string); ok && rec.Body == "" {
			rec.Body = m
			consumed[k] = true
		}
	}
	for _, k := range []string{"time", "timestamp"} {
		if ts, ok := obj[k].(string); ok && !consumed["time"] {
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				rec.TimeUnixNano = uint64(t.UnixNano()) //nolint:gosec // a post-1970 time
				consumed[k], consumed["time"] = true, true
			}
		}
	}
	if id, ok := obj["requestId"].(string); ok && id != "" {
		rec.Attrs = append(rec.Attrs, strAttr("requestId", id), strAttr("faas.invocation_id", id))
		consumed["requestId"] = true
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		if !consumed[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(rec.Attrs) >= maxLiftedAttrs {
			break
		}
		if a, ok := anyAttr(k, obj[k]); ok {
			rec.Attrs = append(rec.Attrs, a)
		}
	}
}

// hexField decodes obj[key] as n bytes of hex; an all-zero value is invalid
// in W3C trace context (not for trace_flags, which may be 00).
func hexField(obj map[string]any, key string, n int) ([]byte, bool) {
	s, ok := obj[key].(string)
	if !ok || len(s) != 2*n {
		return nil, false
	}
	b, err := hex.DecodeString(strings.ToLower(s))
	if err != nil {
		return nil, false
	}
	if key != "trace_flags" && bytes.Equal(b, make([]byte, n)) {
		return nil, false
	}
	return b, true
}

func severityOf(level string) (int32, bool) {
	switch strings.ToUpper(strings.TrimSpace(level)) {
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

func anyAttr(k string, v any) (Attr, bool) {
	switch x := v.(type) {
	case nil:
		return Attr{}, false
	case string:
		return strAttr(k, x), true
	case bool:
		return strAttr(k, strconv.FormatBool(x)), true
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return intAttr(k, n), true
		}
		if f, err := x.Float64(); err == nil {
			return floatAttr(k, f), true
		}
		return strAttr(k, x.String()), true
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return Attr{}, false
		}
		return strAttr(k, string(b)), true
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
// to function records that have none.
func (t *Telemetry) correlate(ev TelemetryEvent, rec *LogRecord) {
	iv := &t.inv
	iv.mu.Lock()
	defer iv.mu.Unlock()
	switch {
	case ev.Type == "platform.start":
		iv.requestID, iv.traceID, iv.spanID, iv.flags = "", nil, nil, 0
		var p platformRecord
		if json.Unmarshal(ev.Record, &p) == nil {
			iv.requestID = p.RequestID
			iv.traceID, iv.spanID, iv.flags = traceContext(&p)
		}
	case ev.Type == "platform.runtimeDone" || ev.Type == "platform.report":
		iv.requestID, iv.traceID, iv.spanID, iv.flags = "", nil, nil, 0
	case ev.Type == "function" && len(rec.TraceID) == 0 && len(iv.traceID) > 0:
		for _, a := range rec.Attrs {
			if a.Key == "requestId" && a.Str != iv.requestID {
				return // a line from another invocation: no guess
			}
		}
		rec.TraceID, rec.SpanID, rec.Flags = iv.traceID, iv.spanID, iv.flags
	}
}
