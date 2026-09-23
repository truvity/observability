# observability

A self-hosted observability stack for Kubernetes estates, as reusable
mechanism: VictoriaMetrics, VictoriaLogs and VictoriaTraces as a
zone-redundant pair behind vmauth, tenancy as a label pair enforced at the
door from the caller's own OIDC token, the collectors that stamp it, the
alert rules that catch a store or a backup failing silently, and Grafana
wired to forward the signed-in user's identity.

Nothing is published yet. The first release lands the `platform-alerts`
chart; the stack and emitter charts and the tenancy library follow.

## Licence

MIT, see [LICENSE](LICENSE).
