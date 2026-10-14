package lambdaext_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/truvity/observability/lambdaext"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
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
	stamp := fixedNow.Add(-time.Minute).Format(time.RFC3339Nano)
	line := `{"time":"` + stamp + `","level":"warn","msg":"retrying","trace_id":"` + liftTrace + `","span_id":"` + liftSpan +
		`","attempt":3,"ratio":0.5,"ok":true,"user":{"id":"u1"},"gone":null}`
	rec := mapOne(t, fnLine(t, line))
	if hex.EncodeToString(rec.TraceID) != liftTrace || hex.EncodeToString(rec.SpanID) != liftSpan {
		t.Fatalf("trace %x span %x", rec.TraceID, rec.SpanID)
	}
	if rec.Severity != lambdaext.SeverityWarn || rec.Body != "retrying" {
		t.Fatalf("severity %d body %q", rec.Severity, rec.Body)
	}
	if want := uint64(fixedNow.Add(-time.Minute).UnixNano()); rec.TimeUnixNano != want {
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
	stamp := fixedNow.Add(-time.Second).Format(time.RFC3339Nano)
	rec := mapOne(t, `{"time":"2022-10-12T00:03:50.000Z","type":"function","record":{"timestamp":"`+stamp+`",`+
		`"level":"INFO","requestId":"r9","message":"hi","trace_id":"`+liftTrace+`"}}`)
	if hex.EncodeToString(rec.TraceID) != liftTrace || rec.Body != "hi" || attrs(rec)["log.attr.requestId"] != "r9" ||
		rec.TimeUnixNano != uint64(fixedNow.Add(-time.Second).UnixNano()) {
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
	rec := mapOne(t, fnLine(t, `{"level":35,"msg":"m"}`))
	if rec.Severity != lambdaext.SeverityInfo || attrs(rec)["level"] != int64(35) {
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
	rec := mapOne(t, fnLine(t, b.String()))
	if n := len(rec.Attrs); n != 1+64+1 { // event.name, the 64, the marker
		t.Fatalf("%d attributes", n)
	}
	if attrs(rec)["log.attrs_truncated"] != "true" {
		t.Fatal("the cap must say it bit")
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

func TestFunctionLogHugeOrDeepLineIsCutNotParsed(t *testing.T) {
	huge := `{"trace_id":"` + liftTrace + `","a":[` + strings.Repeat("1,", 512<<10) + `1]}` // over 1 MiB
	deep := `{"trace_id":"` + liftTrace + `","a":` + strings.Repeat("[", 5000) + strings.Repeat("]", 5000) + `}`
	for name, line := range map[string]string{"huge": huge, "deep": deep + strings.Repeat(" ", 70<<10)} {
		rec := mapOne(t, fnLine(t, line))
		if len(rec.Body) > 64<<10 || rec.TraceID != nil || attrs(rec)["log.truncated"] != "true" || len(rec.Attrs) != 2 {
			t.Errorf("%s: body %d bytes, trace %x, attrs %v", name, len(rec.Body), rec.TraceID, attrs(rec))
		}
	}
	// Deep but small: parsed or not, it is bounded and never panics.
	if rec := mapOne(t, fnLine(t, deep)); len(rec.Body) > 64<<10 || len(rec.Attrs) > 70 {
		t.Errorf("deep: %d attrs", len(rec.Attrs))
	}
	// The same cut for a non-JSON line and for an object record.
	if rec := mapOne(t, fnLine(t, strings.Repeat("é", 100<<10))); len(rec.Body) > 64<<10 || !utf8.ValidString(rec.Body) ||
		attrs(rec)["log.truncated"] != "true" {
		t.Errorf("text: %d bytes valid=%v", len(rec.Body), utf8.ValidString(rec.Body))
	}
	obj := `{"time":"2022-10-12T00:03:51.000Z","type":"function","record":{"message":"` + strings.Repeat("x", 100<<10) + `"}}`
	if rec := mapOne(t, obj); len(rec.Body) > 64<<10 || attrs(rec)["log.truncated"] != "true" {
		t.Errorf("object record: %d bytes", len(rec.Body))
	}
}

func TestFunctionLogLongKeysAndValuesAreCut(t *testing.T) {
	long := strings.Repeat("v", 10<<10)
	rec := mapOne(t, fnLine(t, `{"msg":"m","`+strings.Repeat("k", 500)+`":"x","big":"`+long+`","nested":{"a":"`+long+`"}}`))
	for _, a := range rec.Attrs {
		if len(a.Key) > 128 || len(a.Str) > 4<<10+len("...[truncated]") {
			t.Errorf("attribute %q is %d/%d bytes", a.Key[:min(10, len(a.Key))], len(a.Key), len(a.Str))
		}
	}
	if !strings.HasSuffix(attrs(rec)["big"].(string), "...[truncated]") {
		t.Error("a cut value must say so")
	}
}

func TestFunctionLogTimeWindow(t *testing.T) {
	event := time.Date(2022, 10, 12, 0, 3, 51, 0, time.UTC) // fnLine's event time
	for name, c := range map[string]struct {
		stamp string
		ok    bool
	}{
		"a minute ago":    {fixedNow.Add(-time.Minute).Format(time.RFC3339Nano), true},
		"six days ago":    {fixedNow.Add(-6 * 24 * time.Hour).Format(time.RFC3339Nano), true},
		"four minutes on": {fixedNow.Add(4 * time.Minute).Format(time.RFC3339Nano), true},
		"eight days ago":  {fixedNow.Add(-8 * 24 * time.Hour).Format(time.RFC3339Nano), false},
		"six minutes on":  {fixedNow.Add(6 * time.Minute).Format(time.RFC3339Nano), false},
		"year 1":          {"0001-01-01T00:00:00Z", false},
		"before 1970":     {"1960-01-01T00:00:00Z", false},
		"far future":      {"9999-12-31T23:59:59Z", false},
		"year 2339 wraps": {"2339-01-01T00:00:00Z", false},
		"not a timestamp": {"yesterday", false},
	} {
		rec := mapOne(t, fnLine(t, `{"msg":"m","time":"`+c.stamp+`"}`))
		got := time.Unix(0, int64(rec.TimeUnixNano)).UTC()
		if c.ok {
			if want, _ := time.Parse(time.RFC3339Nano, c.stamp); !got.Equal(want) || attrs(rec)["log.time"] != nil {
				t.Errorf("%s: %v attrs %v", name, got, attrs(rec))
			}
			continue
		}
		if !got.Equal(event) {
			t.Errorf("%s: kept %v, want the event time", name, got)
		}
		if name != "not a timestamp" && attrs(rec)["log.time"] != c.stamp {
			t.Errorf("%s: log.time %v", name, attrs(rec)["log.time"])
		}
	}
}

func TestFunctionLogNumbers(t *testing.T) {
	rec := mapOne(t, fnLine(t, `{"msg":"m","inf":1e999,"big":99999999999999999999,"neg":-9223372036854775809,"i":-5,"f":1.5e3,"exp":1E2}`))
	got := attrs(rec)
	for k, want := range map[string]any{"inf": "1e999", "big": 1e20, "neg": -9.223372036854775808e18, "i": int64(-5), "f": 1500.0, "exp": 100.0} {
		if got[k] != want {
			t.Errorf("%s = %#v, want %#v", k, got[k], want)
		}
	}
}

func TestFunctionLogInvalidUTF8(t *testing.T) {
	bad := string([]byte{0xff, 0xfe})
	head := `{"time":"2022-10-12T00:03:51.000Z","type":"function","record":`
	for name, fx := range map[string]string{
		"text":        head + `"bad ` + bad + ` byte"}`,
		"json string": head + `"{\"msg\":\"a` + bad + `b\",\"k` + bad + `\":\"v` + bad + `\"}"}`,
		"object":      head + `{"message":"a` + bad + `","k` + bad + `":"v` + bad + `"}}`,
	} {
		rec := mapOne(t, fx)
		ok := utf8.ValidString(rec.Body) && rec.Body != ""
		for _, a := range rec.Attrs {
			ok = ok && utf8.ValidString(a.Key) && utf8.ValidString(a.Str)
		}
		if !ok {
			t.Errorf("%s: invalid UTF8 would fail the whole export: %q %v", name, rec.Body, rec.Attrs)
		}
	}
}

func TestFunctionLogReservedAndDuplicateKeys(t *testing.T) {
	rec := mapOne(t, fnLine(t, `{"msg":"m","event.name":"x","service.name":"evil","faas.name":"f","faas.invocation_id":"i","cloud.region":"r",`+
		`"log.time":"z","k8s.pod":"p","otel.x":"o","telemetry.sdk":"s","host.name":"h","a":1,"a":2,"requestId":"r1"}`))
	seen := map[string]int{}
	for _, a := range rec.Attrs {
		seen[a.Key]++
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("attribute %s appears %d times", k, n)
		}
	}
	got := attrs(rec)
	if got["event.name"] != "function" {
		t.Errorf("event.name = %v", got["event.name"])
	}
	for _, k := range []string{"event.name", "service.name", "faas.name", "faas.invocation_id",
		"cloud.region", "log.time", "k8s.pod", "otel.x", "telemetry.sdk", "host.name"} {
		if k == "event.name" {
			if got["log.attr."+k] != "x" {
				t.Errorf("log.attr.%s = %v", k, got["log.attr."+k])
			}
			continue
		}
		if _, leaked := got[k]; leaked {
			t.Errorf("%s was kept as the extension's own", k)
		}
		if _, kept := got["log.attr."+k]; !kept {
			t.Errorf("log.attr.%s is missing", k)
		}
	}
	if got["a"] != int64(2) || got["log.attr.requestId"] != "r1" || got["requestId"] != nil || got["faas.invocation_id"] != nil {
		t.Errorf("attrs %v", got)
	}
}

func TestFunctionLogAttributesKeepLineOrderWhenCapped(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"msg":"m","zz_first":1`)
	for i := range 100 {
		b.WriteString(`,"a` + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `":1`)
	}
	b.WriteString("}")
	got := attrs(mapOne(t, fnLine(t, b.String())))
	if got["zz_first"] != int64(1) || got["log.attrs_truncated"] != "true" {
		t.Errorf("the first key must survive the cap: %v", got)
	}
}

func TestFunctionLogNonStringMessageStaysTheLine(t *testing.T) {
	line := `{"msg":{"a":1},"k":1}`
	rec := mapOne(t, fnLine(t, line))
	if rec.Body != line || attrs(rec)["msg"] != `{"a":1}` {
		t.Errorf("body %q attrs %v", rec.Body, attrs(rec))
	}
}

func TestFunctionLogTraceIDWithoutSpanID(t *testing.T) {
	rec := mapOne(t, fnLine(t, `{"msg":"m","trace_id":"`+liftTrace+`"}`))
	if hex.EncodeToString(rec.TraceID) != liftTrace || rec.SpanID != nil {
		t.Errorf("trace %x span %x", rec.TraceID, rec.SpanID)
	}
}

func TestFunctionLogLevelForms(t *testing.T) {
	for line, want := range map[string]int32{
		`{"level":"ERROR+2","msg":"m"}`:      lambdaext.SeverityError,
		`{"level":"WARN+1","msg":"m"}`:       lambdaext.SeverityWarn,
		`{"level":"DEBUG\u002d4","msg":"m"}`: lambdaext.SeverityDebug,
		`{"level":"INFO+3","msg":"m"}`:       lambdaext.SeverityInfo,
		`{"level":10,"msg":"m"}`:             lambdaext.SeverityDebug,
		`{"level":20,"msg":"m"}`:             lambdaext.SeverityDebug,
		`{"level":30,"msg":"m"}`:             lambdaext.SeverityInfo,
		`{"level":40,"msg":"m"}`:             lambdaext.SeverityWarn,
		`{"level":50,"msg":"m"}`:             lambdaext.SeverityError,
		`{"level":60,"msg":"m"}`:             lambdaext.SeverityFatal,
		`{"severity":"error","msg":"m"}`:     lambdaext.SeverityError,
		`{"Level":"WARNING","msg":"m"}`:      lambdaext.SeverityWarn,
		`{"level":"verbose","msg":"m"}`:      lambdaext.SeverityInfo,
		`{"level":"ERROR-x","msg":"m"}`:      lambdaext.SeverityInfo,
	} {
		if got := mapOne(t, fnLine(t, line)).Severity; got != want {
			t.Errorf("%s: severity %d, want %d", line, got, want)
		}
	}
	stamp := fixedNow.Add(-time.Second).Format(time.RFC3339Nano)
	rec := mapOne(t, fnLine(t, `{"Message":"capital","ts":"`+stamp+`"}`))
	if rec.Body != "capital" || rec.TimeUnixNano != uint64(fixedNow.Add(-time.Second).UnixNano()) {
		t.Errorf("body %q time %d", rec.Body, rec.TimeUnixNano)
	}
}

func TestFunctionLogRequestIDComesFromTheInvocation(t *testing.T) {
	up := newFakeUpstream(t)
	tele := &lambdaext.Telemetry{Upstream: up.URL, Tokens: &staticTokens{token: "tok"}}
	h := httptest.NewServer(tele)
	defer h.Close()
	const req = "6d68ca91-49c9-448d-89b8-7ca3e6dc66aa" // fxStart's request
	body := "[" + fxStart + "," +
		fnLine(t, `{"msg":"forged","requestId":"someone-else","faas.invocation_id":"someone-else"}`) + "," +
		fnLine(t, `{"msg":"named","requestId":"`+req+`"}`) + "," +
		fnLine(t, `{"msg":"silent"}`) + "," +
		fxRuntimeDone + "," +
		fnLine(t, `{"msg":"late","requestId":"`+req+`"}`) + "]"
	resp, err := http.Post(h.URL, "application/json", strings.NewReader(body))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%v %v", resp, err)
	}
	_ = resp.Body.Close()
	eventually(t, "the export", func() bool { return len(up.seen()) == 1 })
	_, recs := decodeLogs(t, up.seen()[0])
	by := map[string]*logspb.LogRecord{}
	for _, r := range recs {
		by[r.GetBody().GetStringValue()] = r
	}
	has := func(r *logspb.LogRecord, key string) (string, bool) {
		v, ok := wireAttrs(r.Attributes)[key]
		s, _ := v.(string)
		return s, ok
	}
	for _, name := range []string{"forged", "late"} {
		if v, ok := has(by[name], "faas.invocation_id"); ok {
			t.Errorf("%s: faas.invocation_id = %q from the line", name, v)
		}
		if _, ok := has(by[name], "requestId"); ok {
			t.Errorf("%s: requestId must not come from the line", name)
		}
	}
	if v, _ := has(by["forged"], "log.attr.requestId"); v != "someone-else" {
		t.Errorf("the line's own requestId is kept as log.attr.requestId, got %q", v)
	}
	for _, name := range []string{"named", "silent"} {
		if v, _ := has(by[name], "faas.invocation_id"); v != req {
			t.Errorf("%s: faas.invocation_id = %q, want the invocation in flight", name, v)
		}
	}
}

func TestTelemetryQueueByteBudgetDropsOldestAndSaysSo(t *testing.T) {
	up := newFakeUpstream(t)
	tele := &lambdaext.Telemetry{Upstream: up.URL, Tokens: &staticTokens{token: "tok"}, QueueBytes: 250 << 10,
		Resource: lambdaext.Resource(func(string) string { return "" }), Now: func() time.Time { return fixedNow }}
	var recs []*lambdaext.LogRecord
	for i := range 6 { // 6 x 100 KiB against a 250 KiB budget
		recs = append(recs, &lambdaext.LogRecord{Body: fmt.Sprintf("r%d", i) + strings.Repeat("x", 100<<10)})
	}
	tele.Enqueue(recs)
	if d := tele.Dropped(); d != 4 {
		t.Fatalf("dropped %d, want 4 (two 100 KiB records fit)", d)
	}
	if err := tele.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, got := decodeLogs(t, up.seen()[0])
	if len(got) != 3 || !strings.Contains(got[0].Body.GetStringValue(), "dropped 4 Lambda telemetry records") ||
		!strings.HasPrefix(got[1].Body.GetStringValue(), "r4") || !strings.HasPrefix(got[2].Body.GetStringValue(), "r5") {
		t.Fatalf("records %d", len(got))
	}
	// Once taken, the budget is free again.
	tele.Enqueue(recs[:2])
	if tele.Dropped() != 0 {
		t.Fatalf("a drained queue must hold two records again, dropped %d", tele.Dropped())
	}
}

func TestFunctionLogsOffThroughTheFullPath(t *testing.T) {
	rt, sts, up := newFakeRuntime(t), newFakeSTS(t), newFakeUpstream(t)
	issuer := newFakeIssuer(t, 900)
	// The old spelling turns it off too, with the deprecation line.
	ext := startExtension(t, telemetryEnv(t, rt, sts, issuer, up, freeAddr(t), "SLUIS_FUNCTION_LOGS=false"))
	eventually(t, "the subscription", func() bool { return len(rt.subscriptions()) == 1 })
	if sub := rt.subscriptions()[0]; !strings.Contains(sub, `"types":["platform"]`) || strings.Contains(sub, "function") {
		t.Fatalf("subscription: %s", sub)
	}
	if !strings.Contains(ext.logs.String(), "SLUIS_FUNCTION_LOGS is deprecated") {
		t.Fatalf("no deprecation line:\n%s", ext.logs.String())
	}
}

func TestQueueBytesSetting(t *testing.T) {
	get := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if c, err := lambdaext.LoadTelemetryConfig(get(nil)); err != nil || c.QueueBytes != 4<<20 {
		t.Fatalf("default %+v %v", c, err)
	}
	if c, err := lambdaext.LoadTelemetryConfig(get(map[string]string{lambdaext.EnvBufferQueueBytes: " 1048576 "})); err != nil || c.QueueBytes != 1<<20 {
		t.Fatalf("set %+v %v", c, err)
	}
	if _, err := lambdaext.LoadTelemetryConfig(get(map[string]string{lambdaext.EnvBufferQueueBytes: "10"})); err == nil ||
		!strings.Contains(err.Error(), lambdaext.EnvBufferQueueBytes) {
		t.Fatalf("a budget below the batch size must be refused: %v", err)
	}
}

func TestFunctionLogExactly64KiB(t *testing.T) {
	const limit = 64 << 10
	pad := func(prefix, suffix string, total int) string {
		return prefix + strings.Repeat("a", total-len(prefix)-len(suffix)) + suffix
	}
	atLimit := pad(`{"msg":"`, `"}`, limit)
	if rec := mapOne(t, fnLine(t, atLimit)); attrs(rec)["log.truncated"] != nil || len(rec.Body) != limit-len(`{"msg":""}`) {
		t.Errorf("a line of exactly 64 KiB is parsed: body %d, attrs %v", len(rec.Body), attrs(rec)["log.truncated"])
	}
	over := pad(`{"msg":"`, `"}`, limit+1)
	if rec := mapOne(t, fnLine(t, over)); attrs(rec)["log.truncated"] != "true" || len(rec.Body) != limit {
		t.Errorf("one byte over is cut: body %d", len(rec.Body))
	}
	text := strings.Repeat("t", limit)
	if rec := mapOne(t, fnLine(t, text)); attrs(rec)["log.truncated"] != nil || len(rec.Body) != limit {
		t.Errorf("a text line of exactly 64 KiB is whole: body %d", len(rec.Body))
	}
}

func TestFunctionLogLiteralRenamedKeyDoesNotCollide(t *testing.T) {
	rec := mapOne(t, fnLine(t, `{"msg":"m","log.attr.x":"literal","log.x":"renamed","x":"plain"}`))
	seen := map[string]int{}
	for _, a := range rec.Attrs {
		seen[a.Key]++
	}
	got := attrs(rec)
	if got["log.attr.log.attr.x"] != "literal" || got["log.attr.log.x"] != "renamed" || got["x"] != "plain" {
		t.Errorf("attrs %v", got)
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("%s appears %d times", k, n)
		}
	}
}

func TestMapEventPlatformTimeIsRangeChecked(t *testing.T) {
	for _, stamp := range []string{"0001-01-01T00:00:00Z", "1960-01-01T00:00:00Z", "9999-12-31T23:59:59Z", fixedNow.Add(time.Hour).Format(time.RFC3339)} {
		rec := mapOne(t, `{"time":"`+stamp+`","type":"platform.report","record":{"requestId":"r"}}`)
		if rec.TimeUnixNano != uint64(fixedNow.UnixNano()) {
			t.Errorf("%s: time %d, want the observed time", stamp, rec.TimeUnixNano)
		}
	}
}
