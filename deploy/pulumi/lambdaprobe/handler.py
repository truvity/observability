"""lambda-otel-probe: one span, one counter increment and one log line per
invocation, sent as OTLP/HTTP JSON to the access-roster layer's loopback
proxy (127.0.0.1:4318). The layer, not this function, authenticates the
export with the function role's identity.

Standard library only, on purpose: nothing to package, nothing to patch.
Each export is synchronous (the execution environment freezes after the
handler returns) and a failed export fails the invocation, so the Lambda
Errors metric is a second signal beside the telemetry heartbeat itself.
"""

import json
import os
import secrets
import time
import urllib.error
import urllib.request

MARKER = "lambda-otel-probe heartbeat"
ENDPOINT = os.environ.get("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318").rstrip("/")
SERVICE = os.environ.get("OTEL_SERVICE_NAME", "lambda-otel-probe")
ATTEMPTS = 3

RESOURCE = {"attributes": [{"key": "service.name", "value": {"stringValue": SERVICE}}]}
SCOPE = {"name": "lambda-otel-probe"}


def _post(path, body):
    """POST one OTLP/JSON document, retrying the layer's retryable 503."""
    req = urllib.request.Request(
        ENDPOINT + path,
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    last = None
    for attempt in range(ATTEMPTS):
        try:
            with urllib.request.urlopen(req, timeout=5) as resp:
                if 200 <= resp.status < 300:
                    return
                last = "HTTP %d" % resp.status
        except urllib.error.HTTPError as err:
            last = "HTTP %d: %s" % (err.code, err.read(200).decode(errors="replace"))
        except OSError as err:
            last = repr(err)
        if attempt + 1 < ATTEMPTS:
            time.sleep(1 + attempt)
    raise RuntimeError("export %s failed: %s" % (path, last))


def handler(event, context):
    start = time.time_ns()
    trace_id = secrets.token_hex(16)
    span_id = secrets.token_hex(8)
    request_id = getattr(context, "aws_request_id", "")
    end = time.time_ns()

    failures = []
    exports = (
        ("/v1/traces", {"resourceSpans": [{"resource": RESOURCE, "scopeSpans": [{"scope": SCOPE, "spans": [{
            "traceId": trace_id,
            "spanId": span_id,
            "name": "heartbeat",
            "kind": 1,
            "startTimeUnixNano": str(start),
            "endTimeUnixNano": str(end),
            "attributes": [{"key": "probe.marker", "value": {"stringValue": MARKER}}],
        }]}]}]}),
        ("/v1/metrics", {"resourceMetrics": [{"resource": RESOURCE, "scopeMetrics": [{"scope": SCOPE, "metrics": [{
            "name": "lambda_otel_probe.heartbeats",
            "unit": "{invocation}",
            "sum": {
                "aggregationTemporality": 1,  # delta: one increment per invocation
                "isMonotonic": True,
                "dataPoints": [{"startTimeUnixNano": str(start), "timeUnixNano": str(end), "asInt": "1"}],
            },
        }]}]}]}),
        ("/v1/logs", {"resourceLogs": [{"resource": RESOURCE, "scopeLogs": [{"scope": SCOPE, "logRecords": [{
            "timeUnixNano": str(end),
            "severityNumber": 9,
            "severityText": "INFO",
            "body": {"stringValue": MARKER},
            "attributes": [{"key": "requestId", "value": {"stringValue": request_id}}],
            "traceId": trace_id,
            "spanId": span_id,
        }]}]}]}),
    )
    for path, body in exports:
        try:
            _post(path, body)
        except RuntimeError as err:
            failures.append(str(err))

    if failures:
        raise RuntimeError("; ".join(failures))
    return {"marker": MARKER, "trace_id": trace_id}
