package lambdaext_test

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/truvity/observability/lambdaext"
)

// fnLine is a function event whose record is the string line (the text log
// format, or a JSON line printed to stdout).
func fnLine(t *testing.T, line string) string {
	t.Helper()
	b, err := json.Marshal(line)
	if err != nil {
		t.Fatal(err)
	}
	return `{"time":"2022-10-12T00:03:51.000Z","type":"function","record":` + string(b) + `}`
}

const (
	liftTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	liftSpan  = "00f067aa0ba902b7"
)

func TestFunctionLogJSONLineIsLifted(t *testing.T) {
	line := `{"time":"2026-10-14T10:11:12.5Z","level":"warn","msg":"retrying","trace_id":"` + liftTrace + `","span_id":"` + liftSpan +
		`","attempt":3,"ratio":0.5,"ok":true,"user":{"id":"u1"},"gone":null}`
	rec := mapOne(t, fnLine(t, line))
	if hex.EncodeToString(rec.TraceID) != liftTrace || hex.EncodeToString(rec.SpanID) != liftSpan {
		t.Fatalf("trace %x span %x", rec.TraceID, rec.SpanID)
	}
	if rec.Severity != lambdaext.SeverityWarn || rec.Body != "retrying" {
		t.Fatalf("severity %d body %q", rec.Severity, rec.Body)
	}
	if want := uint64(time.Date(2026, 10, 14, 10, 11, 12, 500_000_000, time.UTC).UnixNano()); rec.TimeUnixNano != want {
		t.Fatalf("time %d, want %d", rec.TimeUnixNano, want)
	}
	got := attrs(rec)
	for k, v := range map[string]any{"attempt": int64(3), "ratio": 0.5, "ok": "true", "user": `{"id":"u1"}`, "event.name": "function"} {
		if got[k] != v {
			t.Errorf("attribute %s = %v, want %v (all: %v)", k, got[k], v, got)
		}
	}
	for _, k := range []string{"trace_id", "span_id", "level", "msg", "time", "gone"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s must be lifted, not kept as an attribute", k)
		}
	}
}

func TestFunctionLogJSONObjectRecordIsLifted(t *testing.T) {
	// The Lambda JSON log format delivers an object, not a string.
	rec := mapOne(t, `{"time":"2022-10-12T00:03:50.000Z","type":"function","record":{"timestamp":"2022-10-12T00:03:49.000Z",`+
		`"level":"INFO","requestId":"r9","message":"hi","trace_id":"`+liftTrace+`"}}`)
	if hex.EncodeToString(rec.TraceID) != liftTrace || rec.Body != "hi" || attrs(rec)["requestId"] != "r9" ||
		rec.TimeUnixNano != uint64(time.Date(2022, 10, 12, 0, 3, 49, 0, time.UTC).UnixNano()) {
		t.Fatalf("%+v", rec)
	}
}

func TestFunctionLogNonJSONStaysText(t *testing.T) {
	for _, line := range []string{
		"plain text with a { brace", `{"truncated":`, `["an","array"]`, `{"a":1} trailing`, "null", "",
	} {
		rec := mapOne(t, fnLine(t, line))
		if rec.Body != line || rec.TraceID != nil || rec.Severity != lambdaext.SeverityInfo {
			t.Errorf("%q: body %q trace %x sev %d", line, rec.Body, rec.TraceID, rec.Severity)
		}
		if a := attrs(rec); len(a) != 1 {
			t.Errorf("%q: unexpected attributes %v", line, a)
		}
	}
}

func TestFunctionLogJSONWithoutMessageKeepsTheLineAsBody(t *testing.T) {
	line := `{"event":"saved","id":7}`
	rec := mapOne(t, fnLine(t, line))
	if rec.Body != line || attrs(rec)["event"] != "saved" || attrs(rec)["id"] != int64(7) {
		t.Fatalf("body %q attrs %v", rec.Body, attrs(rec))
	}
}

func TestFunctionLogInvalidTraceFieldsAreNotLifted(t *testing.T) {
	for name, line := range map[string]string{
		"all zero":  `{"msg":"x","trace_id":"00000000000000000000000000000000","span_id":"` + liftSpan + `"}`,
		"too short": `{"msg":"x","trace_id":"abcd"}`,
		"not hex":   `{"msg":"x","trace_id":"zzf92f3577b34da6a3ce929d0e0e4736"}`,
		"number":    `{"msg":"x","trace_id":12345}`,
		"span only": `{"msg":"x","span_id":"` + liftSpan + `"}`,
	} {
		rec := mapOne(t, fnLine(t, line))
		if rec.TraceID != nil || rec.SpanID != nil || rec.Body != "x" {
			t.Errorf("%s: trace %x span %x body %q", name, rec.TraceID, rec.SpanID, rec.Body)
		}
	}
	// A bad span id keeps the (valid) trace id.
	rec := mapOne(t, fnLine(t, `{"msg":"x","trace_id":"`+liftTrace+`","span_id":"nope"}`))
	if hex.EncodeToString(rec.TraceID) != liftTrace || rec.SpanID != nil {
		t.Errorf("trace %x span %x", rec.TraceID, rec.SpanID)
	}
}

func TestFunctionLogLevels(t *testing.T) {
	for level, want := range map[string]int32{
		"debug": lambdaext.SeverityDebug, "TRACE": lambdaext.SeverityDebug, "Info": lambdaext.SeverityInfo,
		"warning": lambdaext.SeverityWarn, "error": lambdaext.SeverityError, "fatal": lambdaext.SeverityFatal,
	} {
		if got := mapOne(t, fnLine(t, `{"level":"`+level+`","msg":"m"}`)).Severity; got != want {
			t.Errorf("level %s: severity %d, want %d", level, got, want)
		}
	}
	// A level that is not a known word stays visible as an attribute.
	rec := mapOne(t, fnLine(t, `{"level":30,"msg":"m"}`))
	if rec.Severity != lambdaext.SeverityInfo || attrs(rec)["level"] != int64(30) {
		t.Errorf("sev %d attrs %v", rec.Severity, attrs(rec))
	}
}

func TestFunctionLogAttributesAreBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"msg":"many"`)
	for i := range 200 {
		b.WriteString(`,"k` + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `":1`)
	}
	b.WriteString("}")
	if n := len(mapOne(t, fnLine(t, b.String())).Attrs); n > 64 {
		t.Fatalf("%d attributes", n)
	}
}

func TestFunctionLogFallsBackToTheInvocationsXRayTrace(t *testing.T) {
	up := newFakeUpstream(t)
	tele := &lambdaext.Telemetry{Upstream: up.URL, Tokens: &staticTokens{token: "tok"}}
	h := httptest.NewServer(tele)
	defer h.Close()
	otherReq := `{"time":"2022-10-12T00:03:52.000Z","type":"function","record":{"requestId":"someone-else","message":"stray"}}`
	body := "[" + fxStart + "," +
		fnLine(t, "plain text") + "," +
		fnLine(t, `{"msg":"own","trace_id":"`+liftTrace+`","span_id":"`+liftSpan+`"}`) + "," +
		otherReq + "," +
		fxRuntimeDone + "," +
		fnLine(t, "after the invocation") + "]"
	resp, err := http.Post(h.URL, "application/json", strings.NewReader(body))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%v %v", resp, err)
	}
	_ = resp.Body.Close()
	eventually(t, "the export", func() bool { return len(up.seen()) == 1 })
	_, recs := decodeLogs(t, up.seen()[0])
	byBody := map[string]int{}
	for i, r := range recs {
		byBody[r.GetBody().GetStringValue()] = i
	}
	const xray = "5759e988bd862e3fe1be46a994272793"
	if r := recs[byBody["plain text"]]; hex.EncodeToString(r.TraceId) != xray {
		t.Errorf("plain line: trace %x, want the X-Ray one", r.TraceId)
	}
	if r := recs[byBody["own"]]; hex.EncodeToString(r.TraceId) != liftTrace || hex.EncodeToString(r.SpanId) != liftSpan {
		t.Errorf("lifted line: trace %x span %x", r.TraceId, r.SpanId)
	}
	if r := recs[byBody["stray"]]; r.TraceId != nil {
		t.Errorf("a line of another request must not take this trace: %x", r.TraceId)
	}
	if r := recs[byBody["after the invocation"]]; r.TraceId != nil {
		t.Errorf("a line after the invocation must not take its trace: %x", r.TraceId)
	}
}
