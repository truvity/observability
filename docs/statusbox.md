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

A Config built from `external-endpoints:` alone refuses to start: Gatus
panics at boot ("configuration should contain at least one endpoint or
suite") unless at least one ordinary `endpoints:` or `suites:` entry is
present too, and separately refuses any `external-endpoints:` entry with
no `token` ("you must specify a token for each external endpoint") — the
credential the pushing side has to send back on every push. Neither
constraint is optional and neither is enforced by this package; Config
is the estate's own YAML, so both are on the estate to get right. For
`gatus-ops` the first constraint costs nothing: its ordinary `endpoints:`
entries are the same hostname and certificate probes the table below
already asks it to run, so external-endpoints is never actually alone
there — but a Config that tries to make an instance JUST a deadman
receiver, with nothing else configured, will not boot.

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

type Secrets struct {
    TailscaleAuthKey pulumi.StringInput            // one-shot pre-authorised key; required
    TunnelToken      pulumi.StringInput            // required only if any Instance is Public
    AlertURLs        map[string]pulumi.StringInput // keyed name → ${ALERT_URL_<NAME>} in a Config
}

type Args struct {
    Version    string          // a release of this repository; setup.sh is fetched from it
    Instances  []Instance
    Secrets    Secrets
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

Lightsail's user-data field accepts a single physical line — nothing
else. It is not a cloud-init multi-part document and not a shebang
script the way EC2's user-data is: the provider's own worked example
chains every step with `&&` on one line rather than using a shebang at
all, because that is the shape its user-data actually supports, and a
script with real newlines in it is a box that never boots on this
provider. So `CloudInit` never hands the provider plain text: it renders
the whole bootstrap as an ordinary multi-line script, gzips it,
base64-encodes the result, and returns one line that decodes and runs
it — `bash -c "$(echo <blob> | base64 -d | gunzip)"` — whose own text
contains no newline even though what it runs, once decoded on the box,
is the multi-line script an operator can read. Gzip is not just headroom
against the 16 KB limit below; it is also what makes a script with real
structure fit inside a field that admits none.

`Secrets.AlertURLs` is how a Config asks for a push-alert credential
without carrying it as a literal — the mechanism, not one option among
several. Gatus substitutes `${VAR}` inside its own YAML at start-up, so
a Config writes `${ALERT_URL_<NAME>}` (an `AlertURLs` map key,
upper-cased) wherever it wants that value: an external endpoint's
`webhook-url`, for instance. `CloudInit` stages every entry as an
exported environment variable in the boot script; `setup.sh` collects
every `ALERT_URL_*` it finds into a `.env` file beside the box's compose
file, and every instance's compose service names that file under its own
`env_file:` — the directive that actually puts a variable into a
container's environment, which listing `.env` next to a compose file on
its own does not; `${...}` substitution WITHIN the compose file's own
text is a different mechanism and does nothing for a container that
never mentions the variable, which is exactly what `env_file:` is for
here, since `AlertURLs`'s keys are the estate's own and unknown to
`setup.sh` ahead of time. The credential still ends up in the box's
user-data in plain text — see "readable from the instance metadata
service" below — but a Config already written down (in the estate's own
repository, in its catalogue) never has to carry it.

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
  gzips the whole rendered script — instance configs and all, see the
  single-line requirement above — and refuses to render past the limit;
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
