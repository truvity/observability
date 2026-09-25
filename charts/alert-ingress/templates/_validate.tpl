{{/*
Every refusal in this chart.

The rule for what belongs here, and the shapes it does NOT cover,
matches docs/alert-ingress.md#refusals exactly:

  - `topics` empty                        -> validate.topics
  - a mapping with no `alertname`/`severity` -> validate.mappings
  - `heartbeat.match` empty               -> validate.heartbeat
  - `networkPolicy.alertmanagerPeer` empty while enabled -> validate.networkPolicy

`mappings` itself being EMPTY is deliberately absent from this list. The
design page calls it out for the same reason a `stores: []` platform-alerts
release is not refused either: it is legal — every message becomes
CloudEventUnmapped, which is the honest behaviour for an install with no
mapping written yet — and refusing it would make "install this chart
before writing your first mapping" impossible, which is exactly the order
an estate onboards in.

Likewise `selfMonitor: false` renders no PodMonitor and fails nothing: it
is a real choice for an estate that scrapes some other way, documented in
values.yaml rather than blocked here.
*/}}

{{- define "alert-ingress.validate" -}}
{{- include "alert-ingress.validate.topics" . -}}
{{- include "alert-ingress.validate.mappings" . -}}
{{- include "alert-ingress.validate.heartbeat" . -}}
{{- include "alert-ingress.validate.networkPolicy" . -}}
{{- end -}}

{{/*
An open subscription endpoint.

Confirmation is the one outbound request this service makes on the
strength of an incoming message alone: given a signed
SubscriptionConfirmation for ANY topic, an endpoint with no allow-list
would confirm it and start accepting alerts for a topic nobody here
chose. `topics` is the whole of that gate, so it is refused empty rather
than defaulted to "confirm nothing" or "confirm everything" — either
default is a guess a public endpoint should not make silently.
*/}}
{{- define "alert-ingress.validate.topics" -}}
{{- if not .Values.topics -}}
{{- fail "alert-ingress: `topics` is empty. This service confirms a subscription and accepts a notification only for a topic on this list; empty, it is an open endpoint that would confirm ANY topic pointed at it and start accepting whatever that topic publishes. List every topic ARN this install should actually receive from." -}}
{{- end -}}
{{- end -}}

{{/*
A mapping that renders no alertname, or one whose severity is blank.

Both are silent in the same way: the render succeeds, the chart installs,
and the first sign anything is wrong is either an Alertmanager alert with
an empty name (which the routing tree cannot template a summary for) or
one with severity "" — which does not match `critical`, `warning` or
`info` in any route, so it falls through to whatever the tree's OWN
default receiver is. That default exists for the stack's own unclassified
alerts; a cloud mapping landing there by accident reads as router
misconfiguration when the whole route wildcard the mapping never told
anyone.
*/}}
{{- define "alert-ingress.validate.mappings" -}}
{{- range $i, $m := (.Values.mappings | default list) -}}
{{- if not $m.alert.alertname -}}
{{- fail (printf "alert-ingress: mappings[%d] (%s) has no `alert.alertname`. Every Alertmanager alert needs a name the routing tree and every notification template can refer to." $i (default "unnamed" $m.name)) -}}
{{- end -}}
{{- if not $m.alert.severity -}}
{{- fail (printf "alert-ingress: mappings[%d] (%s) has no `alert.severity`. Alertmanager's routing tree keys on this label; blank, it matches none of `critical`, `warning` or `info` and falls through to the tree's own default receiver — which reads as the router misrouting an alert nobody actually misrouted." $i (default "unnamed" $m.name)) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
A heartbeat with nothing to recognise.

The chart's own VMRule (templates/vmrule.yaml) watches
alert_ingress_messages_total{mapping="heartbeat"} for silence, and that
counter only ever moves when a message matches `heartbeat.match`. Empty,
it matches NOTHING (see `matches` in cmd/alert-ingress/match.go: an empty
rule matches everything, not this one — `heartbeat.match` empty is
checked here precisely because that behaviour would be the wrong one for
a deadman), so the counter never moves, the VMRule fires on day one
whether or not the estate ever wired up a scheduled ping, and the
first-ever signal from this whole chart is a false deadman.
*/}}
{{- define "alert-ingress.validate.heartbeat" -}}
{{- if not .Values.heartbeat.match -}}
{{- fail "alert-ingress: `heartbeat.match` is empty. The chart's own VMRule watches this service's counter for the heartbeat mapping to keep moving, and an empty match recognises nothing at all — so the counter never advances and the deadman fires on day one, whether or not the estate ever wired up the scheduled ping. Set it to a shape your scheduled event actually carries, e.g. {\"source\": \"alert-ingress-heartbeat\"}." -}}
{{- end -}}
{{- end -}}

{{/*
A NetworkPolicy that allows egress to nothing.

The chart's whole security claim is "it can reach one thing:
Alertmanager" (docs/alert-ingress.md). An enabled NetworkPolicy with no
`alertmanagerPeer` does not weaken that claim, it makes it FALSE in the
other direction: the Deployment cannot reach Alertmanager either, so
every alert this service tries to post fails, is logged, and is never
counted — a silent delivery failure indistinguishable, from outside the
pod, from an estate that is simply quiet.
*/}}
{{- define "alert-ingress.validate.networkPolicy" -}}
{{- if and .Values.networkPolicy.enabled (not .Values.networkPolicy.alertmanagerPeer) -}}
{{- fail "alert-ingress: `networkPolicy.enabled` is true but `networkPolicy.alertmanagerPeer` is empty, so the rendered NetworkPolicy would allow egress to Alertmanager from NOWHERE. Every alert this service tries to post would fail, be logged, and never be counted — a silent delivery failure that looks, from outside the pod, exactly like a quiet estate. Set it to a peer selecting Alertmanager's own pods, or set `networkPolicy.enabled: false` if this cluster's CNI does not enforce NetworkPolicy at all." -}}
{{- end -}}
{{- end -}}
