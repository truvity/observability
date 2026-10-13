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

Until 0.81.0 only `observability-stack` rendered them. From 0.81.0 either chart
could (the stack by default, or `observability-proxy` once the stack's
`door.source` was `chart`). The release that removed the Door plane from the
stack ([adoption.md](adoption.md), "Removing the Door plane from
observability-stack, and `tenancy.principals`") left `observability-proxy` the
only chart that renders it: the stack renders none of these objects and its
schema refuses the Door's keys. The objects kept their kind, name and
namespace across the move, so a consumer moved them between Applications
without a delete ([adoption.md](adoption.md), "Handing the Door plane to
observability-proxy"). The values keys (`vmauth.*`, `tenancy.*`, the proxy half
of `networkPolicy.*`) are this chart's, with `tenancy.identities` as the one
list of who may read what; the service code (`cmd/tenancy-authz`) and its image
did not change, and `tests/proxy_chart_test.go` holds that the stack renders no
Door object and the proxy renders all of them.

Out of scope here, and planned for later steps: a redesigned identities list
and a people block for the Door. Neither exists in this chart yet.
