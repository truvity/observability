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

Gatus does all four from one static binary and one YAML file: it
probes, it renders a page, and it alerts. Two different mechanisms carry
the first two jobs into it, and the difference between them is worth
being precise about, because an earlier design here picked the wrong one
for the second job:

- Gatus accepts pushed results on an *external endpoint* and alerts when
  the pushes stop (`heartbeat.interval`) — a bare heartbeat, no payload
  shape to agree on, which is genuinely "no adapter": whatever the
  install's alerting pipeline is, "a request landed" or "a request did
  not land" is the entire vocabulary.
- Turning an *internal* alert into a status component's colour is not
  that shape. Alertmanager's own webhook receiver POSTs a fixed JSON
  body — the same shape whether an alert is firing or resolved — to ONE
  URL it is configured with; Gatus's external-endpoint mechanism reads
  success from a QUERY PARAMETER on the request it receives instead
  (`success=true` / `success=false` on the URL Alertmanager would POST
  to). Neither side speaks the other's shape: something would have had
  to sit between them, translating a POST body's alert state into that
  query parameter. That translator is an adapter, and an earlier
  revision of this page did not count it as one — it read "no adapter"
  for the deadman above and assumed the same held here. It does not.
  The design below counts it, and picks PULL instead for exactly this
  job: Gatus's own `[BODY]` conditions read the alerting pipeline's read
  API directly, in its own vocabulary, so there is nothing to translate.
  See "internal → status, pulled" below.

This repository turns "run one or more Gatus instances, joined to a
private network, on a small box" into a package an estate calls.

## The shape

One virtual machine, provisioned by Pulumi from a package here, with no
inbound port open. Today, on it:

```
tailscaled   joins the estate's private network: SSH, the one page below, and its read of the install's alerting state
gatus-ops    ONE page, PRIVATE, reachable only over the tailnet: every company's own component (red/green) alongside cluster infrastructure (every hostname, every certificate's expiry) and the deadman — both read, not received, see "internal → status, pulled"
```

No `cloudflared` and no public page yet: nothing on the box is reachable
from outside the private network. A public, per-company status page —
`gatus-<company>`, on that company's own domain — is a SEPARATE instance
an estate adds LATER, once that hostname is delegated; see "a new
company page" in the table below. `pkg/statusbox`'s own shape
(`Instance.Public`, `Args.Hostnames`, one `cloudflared` ingress rule per
`Public` instance) already carries that step — taking it is a consumer
decision, not a mechanism this package gains later. Nothing about
taking it changes the private page: it keeps every company's own
component and every piece of cluster infrastructure, in one page, for
whoever is watching from inside the estate's own network.

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
    Env              map[string]pulumi.StringInput // keyed name → ${<NAME>} in a Config, verbatim
}

type Args struct {
    Version    string          // a release of this repository; setup.sh is fetched from it
    Instances  []Instance
    Secrets    Secrets
    Hostname   string            // the box's OWN tailnet device name (`tailscale up --hostname=`)
    Hostnames  map[string]string // instance name → public hostname, for the tunnel ingress
}

func CloudInit(ctx *pulumi.Context, a Args) (pulumi.StringOutput, error)

// pkg/statusbox/lightsail: the one provider implemented first. A sibling
// for another provider is a second small package over the same CloudInit.
func NewLightsail(ctx *pulumi.Context, name string, a *LightsailArgs, opts ...pulumi.ResourceOption) (*Box, error)
```

`NewLightsail` creates the instance with the rendered user-data, an
`InstancePublicPorts` that admits **exactly one** port — tailscaled's
own WireGuard port, 41641/udp, from and to `0.0.0.0/0` and `::/0` (the
firewall closed by declaration to everything else: not SSH, not the
status page itself, both of which answer only over the tailnet — see
setup.sh) — and a `Disk` + `DiskAttachment` for `/data` that survives
the instance being replaced.

That one port is not a relaxation of "closed": Lightsail's API refuses
an empty `port_info` outright (the AWS provider: "Not enough list
items. Attribute port_info requires 1 item minimum"), so "nothing
public" has to be one narrow, named port rather than zero. 41641/udp
is authenticated WireGuard only — a peer still has to hold a key
tailscaled will accept — and it is what lets the box take a direct
tailnet connection instead of always relaying through DERP.

Lightsail's launch scripts go through cloud-init like any other
provider's, and cloud-init classifies a user-data payload by its FIRST
LINE alone: `#!` makes it treat the rest as a shellscript and run it at
boot, and anything else — plain text, a bare command, a line that only
happens to decode to a script once it runs — is stored as text/plain
and never executed at all. The one real constraint Lightsail's user-data
field adds on top of that is size, not line count: the 16 KB ceiling
`CloudInit` enforces as `userDataLimit` below. So `CloudInit` renders the
whole bootstrap as an ordinary multi-line script, gzips it,
base64-encodes the result, and returns `#!/bin/bash` followed by a
`bash -c "$(echo <blob> | base64 -d | gunzip)"` line that decodes and
runs it — the shebang line is what makes cloud-init execute the rest in
the first place, not an artefact of a line-count limit that never
existed. Gzip is not about fitting into a single line; it is headroom
against the 16 KB limit below, so a script with real structure — several
instances' worth of Config, every secret staged as an environment
variable — still fits comfortably inside it.

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

`Secrets.Env` is the same mechanism, generalised, for a secret that is
NOT a push-alert credential — Gatus's own `security.oidc.client-secret`,
for instance, which a signed-in ops page needs and an alert page never
did. It differs from `AlertURLs` in exactly one place: a key becomes the
WHOLE variable name a Config writes as `${<NAME>}` — no `ALERT_URL_`
prefix — because there is no one shape ("a push URL for THIS instance's
alerting") to namespace it under. `CloudInit` stages each entry under an
internal `STATUSBOX_ENV_<NAME>` name in the boot script — the prefix a
`write_env` grep tells it apart from every other shell variable already
in scope (`PATH`, `STATUSBOX_VERSION`, ...) by, a problem `ALERT_URL_`
never has because that prefix already IS the variable a Config
references; `setup.sh` strips the prefix back off before writing
the real name into the same `.env` file `AlertURLs` entries land in. A
name colliding with `TS_AUTHKEY`, `TUNNEL_TOKEN`, or the `ALERT_URL_`
namespace is refused in `Args.validate` — two secrets landing in a
Config under the same `${...}` reference is worse discovered at deploy
time than in a container's environment after the fact.

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

Joining the tailnet is not the same as being reachable on it: every
instance is published at `127.0.0.1:<Port>` only (see `write_compose`),
so a peer elsewhere on the tailnet still has nothing to connect to until
something on the box forwards a connection to that loopback port. For
the one instance that is not Public, `setup.sh` also registers a
`tailscale serve --tcp=80` forward to `127.0.0.1:<Port>` — this is
the path a person on the tailnet uses to load `gatus-ops`'s private
page, and it always forwards to tailnet port **80** rather than the
instance's own `Port`, so the page is `http://<Hostname>/` — no port
to remember or paste, the same way MagicDNS already lets an operator
reach the box by name alone. Plain HTTP, not `--https`: the tailnet
is WireGuard-encrypted end to end, so a second TLS termination in
front of a page nothing outside the tailnet can even address buys
nothing. (It is not the path "internal → status, pulled" rides: that
traffic runs the other way, `gatus-ops` DIALING OUT to the install's own
alerting read API — an outbound connection this box's Tailscale client
makes on its own, needing no forward and no listener on the box at all.
`serve` matters here only for the person, not the pull.) A Public
instance is never registered this way: it is reached through
cloudflared alone, and the smallest tailnet surface this box can have is
none of its public pages on it at all. Restricting *who* on the tailnet
may reach the forwarded port is the estate's own tailnet ACL to grant (a
`tag:statusbox` the box's identity carries, and a grant naming whichever
peer needs it) — `setup.sh` forwards the port; it does not decide who
may dial it. The OUTBOUND direction is a second, separate ACL grant —
`tag:statusbox` reaching whatever the estate's alerting read API answers
on — and it is the estate's own ACL to write for the identical reason.

Serving on port 80 is only safe because `Args.validate` refuses a
manifest with more than one non-Public instance: the box serves one
combined private page by design (every company's own component and
every piece of cluster infrastructure belong on the SAME page — see
"The shape" above), and two private instances could not both claim
port 80 on one box regardless. An estate that wants a second private
page runs a second box.

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
  shebang-plus-wrapper shape above — and refuses to render past the
  limit;
- user-data is readable from the instance metadata service by any
  process on the box. The box is single-purpose, the tailnet key is
  one-shot, and an alert URL is rotated if the box is ever anything
  else.

## How the estate wires it

| Job | How |
|---|---|
| deadman, internal → status | **pulled**, both — see "internal → status, pulled" below. Gatus on the box reads the install's own alerting state directly; nothing pushes into the box at all today. |
| the deadman's alert | leaves Gatus on **two** providers — the chat channel, and the box's own read of the alerting pipeline, which does not depend on it |
| external probes | ordinary Gatus `endpoints:`: HTTP status, body conditions, `[CERTIFICATE_EXPIRATION]`, DNS, from outside the estate's accounts |
| the second vantage and the watcher's watcher | the edge provider's health checks: multi-region against the same hostnames, and one against the box's own `/health` |
| a new hostname | a line in the estate's catalogue; the estate's renderer emits a probe and a component; the box is replaced |
| a new company page | later, once a `status.<company domain>` hostname is delegated: a new Public `Instance`, its own tunnel ingress rule — the mechanism above already carries it |

### internal → status, pulled

The deadman and the internal-to-status bridge are the same mechanism now,
read rather than received: an ordinary Gatus `endpoints:` entry whose URL
is the install's OWN alerting read API — `tenancy.alertReaders` in
`charts/observability-stack` mints exactly this bearer token, scoped to
one route (vmalert's `/api/v1/alerts`) and nothing else — with an
`Authorization: Bearer ${ALERT_URL_<KEY>}` header (the same
`Secrets.AlertURLs` mechanism a Config already uses for a credential it
must not carry as a literal, repurposed: the value staged there is a
bearer token here, not a push URL).

Two conditions on that one response body carry both jobs:

- **deadman**: the response's `data.alerts` array is non-empty when
  filtered (server-side, via the read API's own `match[]` parameter) to
  `alertname="Watchdog"` — which this install's own `Watchdog` VMRule
  (`vmalert.watchdog.enabled`, default on) is always evaluating, whether
  or not the install runs Alertmanager at all. Gatus's own condition is
  `len([BODY].data.alerts) > 0`.
- **a company's colour**: the same shape, `match[]` filtered instead to
  `customer_facing="true", company="<code>"` — a rule an estate writes
  in its own alerting rules, not something this repository ships.
  `len([BODY].data.alerts) == 0` is green; anything else is red.

Neither condition needs a translator: the read API's own JSON is what
the condition reads, in the alerting pipeline's own vocabulary, and
`match[]` does the narrowing before the response ever reaches Gatus —
Gatus never has to filter an array of alerts itself, which its own
`[BODY]` condition language has no way to do (dot-notation and `len()`
only; no query that selects one element of an array by a field it
carries). What `match[]` cannot narrow by is an alert's `state`
(pending vs firing) — VictoriaMetrics' vmalert applies it to an alert's
own LABELS only — so a `for:` rule that is merely PENDING also counts
as present here. That is the conservative direction for a status page to
be wrong in, and it is written down rather than glossed over.

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
- a unit test that the firewall admits exactly one public port —
  41641/udp, tailscaled's own — and no TCP port at all;
- `setup.sh` in CI, as above;
- fixtures for the refusals: an instance with no config, two instances
  on one port, a public instance with no hostname, two instances that
  are both not Public, user-data over the limit.

After release, in a consumer: the public pages render behind the edge;
the private page answers only over the private network; stopping the box
fires the edge health check; scaling the install's metrics vmalert to
zero (or blocking the box's read of it) fires the deadman on both
providers within its own probe interval; a config change replaces the
instance and the disk comes back with its history.
