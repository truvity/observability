# alert-ingress: events born outside the cluster

Design for `cmd/alert-ingress` and `charts/alert-ingress`. Not yet
released; this page is the contract.

## The problem it closes

Some events an estate must hear about are born in the cloud provider,
not in a cluster: a threat-detection finding, a sign-in to the root
account, a signing operation on a key that should never sign, a budget
crossing its line. The provider will publish each to a topic. What it
will not do is route them through the same tree, with the same
silences, grouping and channels, as everything the cluster raises.

The alternatives are all a second router: the provider's own chat
integration (one channel per configuration, raw payloads for anything it
does not natively format), a serverless function per event (code in the
provider's runtime that nobody tests), or email (a route that cannot be
proven to deliver). Each is a second place an alert can go and a second
place a silence has to be kept. The design here has one router, and
reduces the cloud to "POST JSON at us".

## The shape

A small HTTP service, one container, behind a public route the estate's
edge provides. It knows two things: the envelope a cloud notification
service wraps a message in, and Alertmanager's `POST /api/v2/alerts`.
It knows nothing about the estate.

```yaml
# charts/alert-ingress values
alertmanager:
  url: http://vmalertmanager-observability-stack.observability.svc:9093

# Topics this receiver will confirm a subscription from. A public
# endpoint that confirms anything can be subscribed to anyone's topic
# and fed alerts, so this list is required and refused empty.
topics:
  - "<the security-alerts topic ARN>"
  - "<the budgets topic ARN>"

# Mapping rules, in order; the first whose `match` holds is applied.
# `match` is a set of JSON-path equalities against the message body.
# `alert` fields are Go templates over the parsed body.
mappings:
  - name: guardduty
    match: {"detail-type": "GuardDuty Finding"}
    alert:
      alertname: CloudSecurityFinding
      severity: '{{ if ge .detail.severity 7.0 }}critical{{ else }}warning{{ end }}'
      labels:
        source: guardduty
        k8s_cluster_name: cloud            # so the routing tree has a key to route on
      annotations:
        summary: '{{ .detail.title }}'
        runbook: cloud-security-finding
  - name: root-login
    match: {"detail-type": "AWS Console Sign In via CloudTrail", "detail.userIdentity.type": "Root"}
    alert: {alertname: RootConsoleLogin, severity: critical, labels: {source: cloudtrail, k8s_cluster_name: cloud}}
  - name: budget
    match: {"Message.budgetName": "*"}
    alert: {alertname: BudgetThresholdCrossed, severity: warning, labels: {source: budgets, k8s_cluster_name: cloud}}

# The heartbeat: a scheduled event the estate publishes through the same
# path, and the rule that fires when it stops arriving.
heartbeat:
  match: {"source": "alert-ingress-heartbeat"}
  interval: 15m
```

`k8s_cluster_name` on a cloud alert is not a cluster; it is the routing
key the tree groups and routes on, and `cloud` (or whatever the estate
names it) is how those alerts get their own channel without a second
tree. The chart documents this rather than inventing a second label.

## What it does with a message, in order

1. **Verify the signature.** Every message carries one; the certificate
   is fetched only from a URL under the provider's own signing domain
   (`sns.<region>.amazonaws.com`), pinned by pattern in the binary. A
   message that fails is counted `rejected` and answered 403.
2. **Confirm a subscription** only if its topic is on the allow-list;
   otherwise count `rejected` and answer 403. Confirmation is the one
   outbound request the service makes.
3. **Match** the body against the mapping rules; render the alert.
4. **Unmapped is still an alert.** A message no rule matches becomes
   `CloudEventUnmapped`, `severity: warning`, with the body in an
   annotation. Never a drop: a drop is the failure mode this repository
   exists to close.
5. **POST** to Alertmanager with a `startsAt` of now and an `endsAt` of
   now + `resolveAfter` (default 1h): cloud events do not resolve, so
   the alert expires rather than lingering.
6. **Count** it: `alert_ingress_messages_total{outcome=received|mapped|unmapped|rejected,mapping=…}`.

## Its own deadman

A notification service retries and then gives up quietly; nothing on
the estate's side is told that a topic stopped delivering. So the chart
asks for one more thing from the estate — a scheduled event, published
through the same topic and path every `heartbeat.interval` — and renders
a `VMRule` beside the Deployment:

```
absent_over_time(alert_ingress_messages_total{mapping="heartbeat"}[2 × interval])
  or  increase(alert_ingress_messages_total{mapping="heartbeat"}[2 × interval]) == 0
```

That rule fires through the router like any other, and it is the only
way to know the path itself died.

## Refusals

| Shape | Why |
|---|---|
| `topics` empty | an open subscription endpoint |
| `mappings` empty | everything unmapped — legal, but a consumer who wrote no mapping did not mean it |
| a mapping with no `severity` | routes to the default tier by accident |
| `heartbeat` unset | the path can die unnoticed |
| a `PodMonitor` not rendered (`selfMonitor: false`) | the counters exist and nobody scrapes them |

## Security properties, stated

- The service accepts only signed messages from one provider's signing
  domain and confirms only allow-listed topics. An attacker who can
  reach the public route can make it count `rejected`.
- It holds no credential to the cloud. Confirmation is a GET to a URL
  the provider supplied inside a signed message — the only outbound
  request that changes anything in the cloud. Verifying a signature makes
  a second kind of outbound request, fetching the (public) certificate
  from the provider's signing domain; that one carries no credential
  either, and is pinned to the same domain by pattern in the binary.
- Inside the cluster it can reach one thing: Alertmanager. The chart
  renders a NetworkPolicy that says so, plus what verifying a signature
  and confirming a subscription both also need — cluster DNS, and HTTPS
  to the cloud provider. Vanilla Kubernetes NetworkPolicy cannot pin
  egress to a hostname, only to a podSelector, a namespaceSelector or a
  CIDR block, so that last rule is honest about being "HTTPS, to
  anywhere" rather than a hostname pin this layer cannot express; the
  actual pin is the one in the binary, above.
- It runs as a non-root static binary from `scratch`.

## Proof, before release

- a unit fixture per source shape (finding, sign-in, budget, alarm state
  change, heartbeat), each asserting the rendered alert;
- a signature fixture: a real signed message verifies, a tampered one
  is rejected, a message with a certificate URL outside the signing
  domain is rejected;
- the unmapped path produces `CloudEventUnmapped`;
- golden render; fixtures for each refusal.

After release, in a consumer: a sample finding and a real sign-in reach
the channel through the router; suspending the scheduled heartbeat fires
the deadman rule.
