{{/*
Every refusal in this chart; docs/oncall.md#refusals lists the same set.
Each is a shape that renders, installs and then pages nobody: a GoAlert
that cannot reach its database or decrypt its own keys, a relay with no
bot, a route to a host the links do not name.
*/}}
{{- define "oncall.validate" -}}
{{- $g := .Values.goalert -}}
{{- if not $g.publicURL -}}
{{- fail "oncall: `goalert.publicURL` is required: every link in a page, the sign-in callback and the integration URLs GoAlert shows are built from it." -}}
{{- end -}}
{{- $u := urlParse $g.publicURL -}}
{{- if or (not (has $u.scheme (list "http" "https"))) (not $u.host) (hasSuffix "/" $g.publicURL) -}}
{{- fail (printf "oncall: `goalert.publicURL` %q must be an absolute http(s) URL without a trailing slash." $g.publicURL) -}}
{{- end -}}
{{- if not $g.dataEncryptionKeySecret.name -}}
{{- fail "oncall: `goalert.dataEncryptionKeySecret.name` is required: without a key GoAlert stores its signing keys and secrets unencrypted." -}}
{{- end -}}
{{- if not $g.database.host -}}
{{- fail "oncall: `goalert.database.host` is required (the read-write Service of the PostgreSQL cluster)." -}}
{{- end -}}
{{- if not $g.database.passwordSecret.name -}}
{{- fail "oncall: `goalert.database.passwordSecret.name` is required." -}}
{{- end -}}
{{- if and $g.database.ca.configMap $g.database.ca.secret -}}
{{- fail "oncall: `goalert.database.ca` names both a configMap and a secret; name one." -}}
{{- end -}}
{{- if and (or $g.database.ca.configMap $g.database.ca.secret) (not (has $g.database.sslMode (list "verify-ca" "verify-full"))) -}}
{{- fail (printf "oncall: `goalert.database.ca` is set but `sslMode` is %q, which never reads it; use verify-ca or verify-full." $g.database.sslMode) -}}
{{- end -}}
{{- if and .Values.relay.enabled (not .Values.relay.secret.name) -}}
{{- fail "oncall: `relay.secret.name` is required while the relay is enabled: it holds the bot token, the shared secret and the recipients." -}}
{{- end -}}
{{- if eq (.Values.relay.port | int) (.Values.relay.metricsPort | int) -}}
{{- fail "oncall: `relay.port` and `relay.metricsPort` must differ: /metrics must not be reachable through the webhook port." -}}
{{- end -}}
{{- if eq (.Values.service.port | int) (.Values.service.metricsPort | int) -}}
{{- fail "oncall: `service.port` and `service.metricsPort` must differ." -}}
{{- end -}}
{{- if .Values.httpRoute.enabled -}}
{{- if not .Values.httpRoute.parentRefs -}}
{{- fail "oncall: `httpRoute.parentRefs` is empty while the route is enabled: a route attached to nothing." -}}
{{- end -}}
{{- if not (has $u.hostname .Values.httpRoute.hostnames) -}}
{{- fail (printf "oncall: `httpRoute.hostnames` must include %q, the host of `goalert.publicURL`: the route would not serve the URL every page links to." $u.hostname) -}}
{{- end -}}
{{- end -}}
{{- with .Values.rules.alertmanagerDeliveryFailing -}}
{{- if and .enabled (not .receiverName) -}}
{{- fail "oncall: `rules.alertmanagerDeliveryFailing.receiverName` is required while the rule is enabled: the Alertmanager webhook receiver that posts to GoAlert." -}}
{{- end -}}
{{- end -}}
{{- if .Values.networkPolicy.enabled -}}
{{- if not .Values.networkPolicy.goalertFrom -}}
{{- fail "oncall: `networkPolicy.goalertFrom` is empty: under the default-deny nothing could reach GoAlert, neither people through the gateway nor Alertmanager." -}}
{{- end -}}
{{- if not .Values.networkPolicy.databasePeers -}}
{{- fail "oncall: `networkPolicy.databasePeers` is empty: under the default-deny GoAlert could not reach its database." -}}
{{- end -}}
{{- end -}}
{{- end -}}
