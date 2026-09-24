# The status box: the watcher outside

Design for `pkg/statusbox` (Pulumi, Go), the `setup.sh` release asset,
and the templates they carry. Not yet released; this page is the
contract.

## The problem it closes

Four jobs are conventionally bought from one vendor: a deadman that
notices when the alerting pipeline has died; probes from outside that
answer "can a customer reach us"; a public status page; and a way for an
internal alert to turn a status component red. Each needs to run
somewhere the estate's own failure cannot reach, and each is small.

Gatus does all four from one static binary and one YAML file:
it probes, it renders a page, it alerts, and — the feature that makes it
the deadman receiver and the status bridge with no adapter — it accepts
pushed results on an *external endpoint* and alerts when the pushes stop
(`heartbeat.interval`). This repository turns "run several Gatus
instances behind a tunnel and a private network on a small box" into a
package an estate calls.

## The shape

One virtual machine, provisioned by Pulumi from a package here, with no
inbound port open. On it:

```
tailscaled            joins the estate's private network: SSH, the ops page, the alerting pipeline's pushes
cloudflared           one tunnel to the edge provider; one ingress rule per public page
gatus-<company> ×N    one public status page per legal entity; components per product, never per cluster
gatus-ops             private: the deadman endpoints, every hostname, every certificate's expiry, the alert bridge
```

Each Gatus is the same image, its own YAML, its own SQLite file on an
attached disk. There is no shared database and there are no replicas:
Gatus has no clustering or leader election, and two instances on one
database are two independent probers that both alert. Availability of
the watcher is a second, independent box in another place — never a
shared store — and it is not the starting point.

### `pkg/statusbox`

```go
// The provider-neutral core: renders cloud-init from a version, the
// secrets, and an instance list. Nothing of the estate is in this
// package; everything of the estate is in the arguments.
type Instance struct {
    Name   string // gatus-<name>
    Port   int
    Public bool   // in the tunnel's ingress, or reachable only privately
    Config string // the Gatus YAML, rendered by the estate
}

type Args struct {
    Version    string          // a release of this repository; setup.sh is fetched from it
    Instances  []Instance
    Secrets    Secrets         // tailnet key, tunnel token, alert URLs — pulumi.StringInput each
    Hostnames  map[string]string // instance name → public hostname, for the tunnel ingress
}

func CloudInit(ctx *pulumi.Context, a Args) (pulumi.StringOutput, error)

// pkg/statusbox/lightsail: the one provider implemented first. A sibling
// for another provider is a second small package over the same CloudInit.
func NewLightsail(ctx *pulumi.Context, name string, a *LightsailArgs, opts ...pulumi.ResourceOption) (*Box, error)
```

`NewLightsail` creates the instance with the rendered user-data, an
`InstancePublicPorts` with an **empty** port list (the firewall closed
by declaration), and a `Disk` + `DiskAttachment` for `/data` that
survives the instance being replaced.

### Checksum at deploy, verify at boot

The consumer pins only `Version`. The package fetches that release's
`checksums.txt` at deploy time and bakes the `setup.sh` sha into the
user-data; the box downloads the script from the release and refuses to
run it if the sha does not match. One input, no hand-copied hashes, and
`curl | sh` becomes content-addressed rather than a moving target.

### `setup.sh`

Idempotent, `set -euo pipefail`, a release asset. It installs a
container runtime, `tailscale` (with a one-shot pre-authorised key),
`cloudflared` (with the tunnel token), unpacks the instance configs,
writes a compose file and one systemd unit, and starts it. It knows no
hostname; the hostnames are in the tunnel ingress the estate's edge
configuration owns.

CI runs it on a plain Ubuntu runner with a fixture config and asserts
every instance answers `/health`. The first run of the script must not
be on the box, on a bad day.

## Immutable, by construction

The provider's user-data is applied once, at creation. So a change to
any instance's config **replaces the instance**: about two minutes of
status-page blip, the disk reattached, no history lost. The alternative
— the box pulling its config — needs a credential on the box to pull
with, and the provider chosen first offers no instance role to hold one.
Immutability is the honest design; config changes here are rare.

Two consequences, documented so nobody rediscovers them:

- user-data has a size limit (16 KB on the first provider); the renderer
  gzips the instance configs, and refuses to render past the limit;
- user-data is readable from the instance metadata service by any
  process on the box. The box is single-purpose, the tailnet key is
  one-shot, and an alert URL is rotated if the box is ever anything
  else.

## How the estate wires it

| Job | How |
|---|---|
| deadman | the install's Alertmanager routes `Watchdog` to a webhook that pushes `gatus-ops`'s external endpoint, over the private network, every minute; the endpoint's `heartbeat.interval` is 5m; silence alerts |
| the deadman's alert | leaves Gatus on **two** providers — the chat channel, and a push service that does not depend on it |
| external probes | `gatus-<company>` endpoints: HTTP status, body conditions, `[CERTIFICATE_EXPIRATION]`, DNS, from outside the estate's accounts |
| the second vantage and the watcher's watcher | the edge provider's health checks: multi-region against the same hostnames, and one against the box's own `/health` |
| internal → status | Alertmanager's `also:` route sends `severity=critical, customer_facing=true` to a webhook that pushes the matching company page's external endpoint with `success=false`; resolution pushes `success=true` |
| a new hostname | a line in the estate's catalogue; the estate's renderer emits a probe and a component; the box is replaced |

## What the estate accepts, and what it does not

The box is outside every cluster, and — on the first provider — inside
the same cloud account structure as the estate, in a separate account
and region. A running instance survives the cloud's control-plane
mistakes (an IAM or organisation policy stops API calls, not processes);
what it does not survive is account suspension and a regional
coincidence. An estate that will not accept that runs the same package
on a second provider; the core is provider-neutral for exactly that.

Rejected, with the reason, so nobody re-derives it: a container
platform at the edge provider (ephemeral disk, a deploy tool with no
infrastructure-as-code path, and it collapses the edge and the watcher
into one party); a UI-driven uptime tool (fails config-as-code at the
door); Gatus replicas with a shared database (coordinates nothing).

## Proof, before release

- golden render of the cloud-init for a two-instance fixture;
- a unit test that the public ports list is empty;
- `setup.sh` in CI, as above;
- fixtures for the refusals: an instance with no config, two instances
  on one port, a public instance with no hostname, user-data over the
  limit.

After release, in a consumer: the public pages render behind the edge;
the private page answers only over the private network; stopping the box
fires the edge health check; scaling the install's Alertmanager to zero
fires the deadman on both providers within five minutes; a config change
replaces the instance and the disk comes back with its history.
