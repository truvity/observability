# The Door plane: `charts/observability-proxy`

Every read of the stores, and every cross-cluster write, goes through one
proxy. That proxy and what it needs to decide are the **Door**:

- **vmauth** (the `VMAuth`) in front of the stores, with a `VMUser` per
  principal, per reader, per writer and per alert reader, and the vm-access
  reader (`tenancy.vmAccessReader`);
- **tenancy-authz** (`tenancy.authz`, opt-in; [tenancy-authz.md](tenancy-authz.md)):
  the Envoy external authorisation service that unions a person's groups into
  one `vm_access` token, with its configuration, Service, Deployment,
  PodDisruptionBudget, ServiceMonitor, the signing-key generator and the
  gateway's SecurityPolicy;
- the NetworkPolicies of the proxy and of tenancy-authz.

Until 0.81.0 only `observability-stack` renders them. From 0.81.0 either chart
can: `observability-stack` by default, or `observability-proxy` once the stack's
`door.source` is `chart`. The objects keep their kind, name and namespace
whichever chart owns them, so a consumer moves them between Applications
without a delete ([adoption.md](adoption.md), "Handing the Door plane to observability-proxy"). This is a move
and nothing else: the values keys are the stack's (`vmauth.*`, `tenancy.*`, the
proxy half of `networkPolicy.*`), the service code (`cmd/tenancy-authz`) and its
image are unchanged, and `tests/proxy_chart_test.go` holds the two charts'
renders byte-identical.

Out of scope here, and planned for later steps: a redesigned identities list
and a people block for the Door. Neither exists in this chart yet.
