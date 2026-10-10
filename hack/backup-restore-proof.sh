#!/usr/bin/env bash
# hack/backup-restore-proof.sh — REAL proof, in Docker, of the
# `backup.auth.mode: credentialProcess` path end to end, plus the
# restore docs/reference.md's "Restore" section documents, plus
# (assertion only, see below) that `backup.seLinuxLevel` renders onto
# both the backup CronJob and the VMSingle CR it mirrors against.
#
# adobe/s3mock stands in for an S3-compatible store that is not AWS —
# the shape Cloudflare R2 itself is, the worked example this chart's
# docs use (path-style addressing only, exactly what
# `s3ForcePathStyle` forces). A real VictoriaMetrics single node is
# seeded with one series, and the ACTUAL vmbackup image
# `charts/observability-stack` pins runs with the RENDERED args, env
# and AWS config file `charts/observability-stack/templates/backup.yaml`
# produces for credentialProcess mode against it — not a hand-typed
# stand-in command: this script renders the chart with `helm template`
# and parses the CronJob/ConfigMap/ServiceAccount back out of that
# output. The one exception is `-snapshot.createURL`, whose rendered
# value embeds a Kubernetes Service DNS name and `$(VM_USERNAME)`/
# `$(VM_PASSWORD)` placeholders a real Secret and kubelet's own env
# substitution would resolve in a cluster; here it is resolved the same
# way, by hand, to this proof's own container name and credentials —
# every other rendered arg (`-storageDataPath`, `-customS3Endpoint`,
# `-s3ForcePathStyle`, `-dst`) runs completely unmodified.
#
# The credential_process command itself is a tiny script, built into a
# throwaway "tools" image the same shape `auth.credentialProcess.toolsImage`
# describes, that prints static object-store credentials in the AWS
# SDK's own JSON format — exactly the contract a real credential-broker
# CLI has to meet, minus the broker. S3Mock accepts any credentials, so
# their values are otherwise arbitrary.
#
# Then vmrestore — same image family, same pinned tag — restores that
# backup into a SECOND, empty VictoriaMetrics instance, which is
# queried for the series the first one was seeded with.
#
# Every HTTP interaction below runs from a throwaway curlimages/curl
# container ON THE SAME DOCKER NETWORK as the thing it is talking to,
# addressed by container name — never through a host-published port.
# The alternative (`-p 127.0.0.1::PORT` plus curl from the host) is
# what this script tried first, and host->container port publishing
# proved unreliable on the box this was developed on (connects hung
# past two minutes while the exact same request from a container on
# the shared network answered instantly) — a Docker userland-proxy
# quirk under this host's load, not anything about the chart. Talking
# container-to-container is also simply the more faithful shape: it is
# how vmbackup itself will always reach both the store and the object
# endpoint in a real cluster.
#
# Needs Docker and python3 (PyYAML). Deliberately NOT part of `check`:
# a one-off, run-by-hand proof, the same reason
# hack/statusbox-ec2-ci.sh's own Docker requirement is not — the
# golden renders (tests/golden/observability-stack/backup-credential-process.yaml)
# and the negative fixtures are the regression gate.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d -t backup-restore-proof.XXXXXX)"

net=backup-restore-proof-net
s3=backup-restore-proof-s3
source_vm=backup-restore-proof-vmsingle-source
restored_vm=backup-restore-proof-vmsingle-restored
tools_img=backup-restore-proof-tools:latest
prober=curlimages/curl:latest
bucket=backup-restore-proof
vm_user=proofuser
vm_pass=proofpass
s3_key=proofs3key
s3_secret=proofs3secretkey12345
vol_source=backup-restore-proof-source-data
vol_tools=backup-restore-proof-tools-vol
vol_restored=backup-restore-proof-restored-data

cleanup() {
  docker rm -f "$s3" "$source_vm" "$restored_vm" >/dev/null 2>&1 || true
  docker volume rm -f "$vol_source" "$vol_tools" "$vol_restored" >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
  docker rmi -f "$tools_img" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

for tool in docker python3; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "hack/backup-restore-proof.sh needs $tool, which is not on PATH" >&2
    exit 1
  }
done
python3 -c 'import yaml' 2>/dev/null || {
  echo "hack/backup-restore-proof.sh needs python3's PyYAML (import yaml)" >&2
  exit 1
}

# curl() { ... } — a throwaway curlimages/curl container on $net, so
# every HTTP call below reaches its target the same way vmbackup itself
# will: by container name on the shared network. Prints the response
# body to stdout, same as a normal curl call.
netcurl() {
  docker run --rm --network "$net" "$prober" curl "$@"
}

echo "hack/backup-restore-proof.sh: rendering charts/observability-stack with backup.auth.mode: credentialProcess against a real S3-compatible endpoint"
cat > "$work/values.yaml" <<EOF
global:
  cluster:
    dnsDomain: cluster.example.
tenancy:
  issuerUrl: https://issuer.example
  audience: example-observability-client
  allowUnfilteredTraceReads: true
  principals:
    - group: example:k8s:viewer
      grants:
        - cluster: example-cluster
          allNamespaces: true
notifications:
  externalUrl: https://grafana.example
  slack:
    webhookSecret: {name: example-slack-webhook, key: url}
  severities:
    critical: {receiver: slack, channel: "#alerts-critical"}
    warning: {receiver: slack, channel: "#alerts"}
backup:
  enabled: true
  destination: "s3://$bucket"
  auth:
    mode: credentialProcess
    credentialProcess:
      command: "/var/run/backup-tools/proof-creds"
      toolsImage:
        repository: backup-restore-proof-tools
        tag: latest
  metrics:
    enabled: true
    s3CustomEndpoint: "http://$s3:9090"
    s3ForcePathStyle: "true"
  logs:
    enabled: false
  traces:
    enabled: false
  # An SELinux MCS level, so this proof also covers docs/reference.md's
  # `backup.seLinuxLevel` — see the assertion below for what it can and
  # cannot prove without a real, SELinux-enforcing kernel.
  seLinuxLevel: "s0:c111,c222"
victoria-metrics-k8s-stack:
  vmsingle:
    spec:
      securityContext:
        seLinuxOptions:
          level: "s0:c111,c222"
EOF

helm template proof "$root/charts/observability-stack" --values "$work/values.yaml" > "$work/rendered.yaml"

cat > "$work/extract.py" <<'PYEOF'
import sys, yaml, json

rendered = open(sys.argv[1]).read()
docs = [d for d in yaml.safe_load_all(rendered) if d]

cronjob = next(d for d in docs if d.get("kind") == "CronJob" and d["metadata"]["name"].endswith("backup-metrics-incremental"))
configmap = next(d for d in docs if d.get("kind") == "ConfigMap" and d["metadata"]["name"].endswith("backup-aws-config"))
sa = next(d for d in docs if d.get("kind") == "ServiceAccount" and "backup" in d["metadata"]["name"])
vmsingle = next(d for d in docs if d.get("kind") == "VMSingle")

pod = cronjob["spec"]["jobTemplate"]["spec"]["template"]["spec"]
container = next(c for c in pod["containers"] if c["name"] == "vmbackup")
init = next(c for c in pod["initContainers"] if c["name"] == "backup-tools")

out = {
    "image": container["image"],
    "args": container["args"],
    "env": {e["name"]: e.get("value", "") for e in container["env"] if "value" in e},
    "initImage": init["image"],
    "initCommand": init["command"],
    "awsConfig": configmap["data"]["config"],
    "serviceAccount": sa["metadata"]["name"],
    # `backup.seLinuxLevel`: rendered on the backup job's OWN pod spec
    # (this chart's own template) and, separately — it is a MIRROR, not
    # something this chart can compute into a vendored dependency's
    # object, see docs/reference.md — on the VMSingle CR's pod-level
    # securityContext. The assertion below is only that the RENDER
    # carries the same string onto both; no SELinux enforcement runs in
    # this Docker-based proof to confirm the kernel would honour it.
    "backupSeLinuxLevel": (((pod.get("securityContext") or {}).get("seLinuxOptions") or {}).get("level")),
    "vmsingleSeLinuxLevel": ((((vmsingle["spec"].get("securityContext") or {}).get("seLinuxOptions")) or {}).get("level")),
}
json.dump(out, sys.stdout)
PYEOF
extracted_json="$(python3 "$work/extract.py" "$work/rendered.yaml")"
echo "hack/backup-restore-proof.sh: extracted from the RENDERED CronJob (this is the chart's own output, not hand-written):"
echo "$extracted_json" | python3 -m json.tool

vmbackup_image="$(echo "$extracted_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["image"])')"
vmrestore_image="${vmbackup_image/vmbackup/vmrestore}"
tools_image_rendered="$(echo "$extracted_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["initImage"])')"
[ "$tools_image_rendered" = "$tools_img" ] || {
  echo "hack/backup-restore-proof.sh: rendered initContainer image ($tools_image_rendered) does not match what this script built ($tools_img)" >&2
  exit 1
}
echo "$extracted_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["awsConfig"])' > "$work/aws-config"
aws_config_file="$(echo "$extracted_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["env"]["AWS_CONFIG_FILE"])')"
sdk_load_config="$(echo "$extracted_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["env"]["AWS_SDK_LOAD_CONFIG"])')"

# `backup.seLinuxLevel` (docs/reference.md): the RENDER carries the same
# MCS level onto both the backup job's own pod spec and the VMSingle
# CR's pod-level securityContext — the two objects this proof already
# extracted from the chart's actual output, above. This is as far as
# this proof (or any Docker container) can go: Docker does not enforce
# SELinux, so nothing here exercises the kernel-level category check
# the value exists for — see docs/safety.md, "SELinux MCS categories,
# and why one value cannot set both sides", for the live incident and
# why that check needs a real SELinux-enforcing node instead.
backup_level="$(echo "$extracted_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["backupSeLinuxLevel"])')"
vmsingle_level="$(echo "$extracted_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["vmsingleSeLinuxLevel"])')"
[ "$backup_level" = "s0:c111,c222" ] || {
  echo "hack/backup-restore-proof.sh: rendered backup CronJob securityContext.seLinuxOptions.level is ${backup_level:-<absent>}, expected s0:c111,c222" >&2
  exit 1
}
[ "$vmsingle_level" = "s0:c111,c222" ] || {
  echo "hack/backup-restore-proof.sh: rendered VMSingle securityContext.seLinuxOptions.level is ${vmsingle_level:-<absent>}, expected s0:c111,c222" >&2
  exit 1
}
echo "hack/backup-restore-proof.sh: rendered backup.seLinuxLevel ($backup_level) matches on both the backup CronJob and the VMSingle CR — real SELinux enforcement is NOT exercised by this Docker-based proof"

echo "hack/backup-restore-proof.sh: vmbackup image the chart pins: $vmbackup_image (vmrestore: $vmrestore_image)"

# Resolve the two rendered args that name a Kubernetes Service and a
# Secret's own placeholders — see the header comment — into this
# proof's real container name and credentials. Every other arg is
# passed through EXACTLY as the chart rendered it.
mapfile -t backup_args < <(echo "$extracted_json" | python3 -c '
import json, sys
for a in json.load(sys.stdin)["args"]:
    if a.startswith("-snapshot.createURL="):
        a = "-snapshot.createURL=http://'"$vm_user"':'"$vm_pass"'@'"$source_vm"':8428/snapshot/create"
    print(a)
')

# NUL-delimited, not newline-delimited: the rendered initCommand's third
# element is `sh -euc`'s own multi-line SCRIPT — splitting on newline
# breaks it into extra array elements, and only the first line
# (`mkdir -p ...`) ever runs as the script; the rest become sh -c's own
# $0/$1..., silently ignored. Reproduced once against this exact bug: the
# tools volume came back with `mkdir` having run and the `cp` never
# executed at all.
mapfile -d '' -t init_command < <(echo "$extracted_json" | python3 -c '
import json, sys
for c in json.load(sys.stdin)["initCommand"]:
    sys.stdout.write(c)
    sys.stdout.write("\0")
')

echo
echo "hack/backup-restore-proof.sh: creating the isolated network and building the credential-process tools image"
docker network create "$net" >/dev/null
docker pull -q "$prober" >/dev/null

mkdir -p "$work/tools-build"
cat > "$work/tools-build/proof-creds" <<EOF
#!/bin/sh
# The worked example this chart's docs point at, minus the broker: a
# real credential_process command exchanges the pod's own token for
# temporary credentials; this one just prints static, dummy
# object-store credentials in the same AWS SDK JSON shape
# (https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-sourcing-external.html).
# S3Mock accepts any credentials, so their VALUES are not the point —
# the SHAPE and the fact that vmbackup's AWS SDK execs this command at
# all, and uses what it prints, is.
set -eu
cat <<JSON
{"Version":1,"AccessKeyId":"$s3_key","SecretAccessKey":"$s3_secret","Expiration":"2027-01-01T00:00:00Z"}
JSON
EOF
chmod +x "$work/tools-build/proof-creds"
cat > "$work/tools-build/Dockerfile" <<'EOF'
FROM alpine:3.22
COPY proof-creds /usr/local/bin/proof-creds
RUN chmod +x /usr/local/bin/proof-creds
EOF
docker build -t "$tools_img" "$work/tools-build" >/dev/null

echo "hack/backup-restore-proof.sh: starting the S3-compatible endpoint (adobe/s3mock, path-style only — exactly what s3ForcePathStyle forces)"
docker run -d --name "$s3" --network "$net" \
  -e COM_ADOBE_TESTING_S3MOCK_STORE_INITIAL_BUCKETS="$bucket" \
  adobe/s3mock:latest >/dev/null

echo "hack/backup-restore-proof.sh: waiting for it to answer on the shared network"
docker run --rm --network "$net" "$prober" sh -c "
  for i in \$(seq 1 90); do
    code=\$(curl -s -o /dev/null -w '%{http_code}' http://$s3:9090/$bucket)
    [ \"\$code\" = 200 ] && exit 0
    sleep 1
  done
  exit 1
" || { echo "hack/backup-restore-proof.sh: S3-compatible endpoint never became healthy" >&2; docker logs "$s3" >&2 || true; exit 1; }
echo "hack/backup-restore-proof.sh: S3-compatible endpoint healthy, bucket s3://$bucket exists"

echo "hack/backup-restore-proof.sh: starting the source VictoriaMetrics ($vmbackup_image's own version family)"
vm_tag="${vmbackup_image##*:}"
docker volume create "$vol_source" >/dev/null
docker run -d --name "$source_vm" --network "$net" \
  -v "$vol_source:/storage" \
  "victoriametrics/victoria-metrics:$vm_tag" \
  -storageDataPath=/storage -retentionPeriod=100y \
  -httpAuth.username="$vm_user" -httpAuth.password="$vm_pass" >/dev/null

docker run --rm --network "$net" "$prober" sh -c "
  for i in \$(seq 1 90); do
    code=\$(curl -s -o /dev/null -w '%{http_code}' http://$source_vm:8428/health)
    [ \"\$code\" != 000 ] && exit 0
    sleep 1
  done
  exit 1
" || { echo "hack/backup-restore-proof.sh: source VictoriaMetrics never came up" >&2; docker logs "$source_vm" >&2 || true; exit 1; }
echo "hack/backup-restore-proof.sh: source VictoriaMetrics healthy"

echo "hack/backup-restore-proof.sh: seeding one series and waiting for it to be indexed"
netcurl -fsS -u "$vm_user:$vm_pass" -X POST "http://$source_vm:8428/api/v1/import/prometheus" \
  --data-binary 'proof_backup_restore_marker{instance="source"} 42' >/dev/null

val="$(docker run --rm --network "$net" "$prober" sh -c "
  for i in \$(seq 1 90); do
    v=\$(curl -fsS -u $vm_user:$vm_pass http://$source_vm:8428/api/v1/query --data-urlencode 'query=proof_backup_restore_marker')
    echo \"\$v\"
    case \"\$v\" in *'\"value\":['*) exit 0 ;; esac
    sleep 1
  done
" | tail -1)"
echo "$val" | python3 -c 'import json,sys; d=json.load(sys.stdin)["data"]["result"]; v=d[0]["value"][1] if d else None; assert v == "42", f"got {v!r}"; print("hack/backup-restore-proof.sh: source has proof_backup_restore_marker = 42")'

echo
echo "hack/backup-restore-proof.sh: copying credential-process tools (mirrors the chart's own initContainer, exact rendered command):"
printf '  %s\n' "${init_command[@]}"
docker volume create "$vol_tools" >/dev/null
docker run --rm -v "$vol_tools:/var/run/backup-tools" "$tools_img" "${init_command[@]}"

echo
echo "hack/backup-restore-proof.sh: running the RENDERED vmbackup command against the S3-compatible endpoint:"
printf '  %s\n' "${backup_args[@]}"
docker run --rm --network "$net" \
  -v "$vol_source:/vm-data:ro" \
  -v "$vol_tools:/var/run/backup-tools:ro" \
  -v "$work/aws-config:/etc/aws/config:ro" \
  -e VM_USERNAME="$vm_user" -e VM_PASSWORD="$vm_pass" \
  -e "AWS_CONFIG_FILE=$aws_config_file" -e "AWS_SDK_LOAD_CONFIG=$sdk_load_config" \
  "$vmbackup_image" "${backup_args[@]}"

echo
echo "hack/backup-restore-proof.sh: confirming the backup landed on the S3-compatible endpoint"
listing="$(docker run --rm --network "$net" \
  -e AWS_ACCESS_KEY_ID="$s3_key" -e AWS_SECRET_ACCESS_KEY="$s3_secret" -e AWS_DEFAULT_REGION=us-east-1 \
  amazon/aws-cli --endpoint-url "http://$s3:9090" s3 ls "s3://$bucket/metrics" --recursive)"
echo "$listing"
[ -n "$listing" ] || { echo "hack/backup-restore-proof.sh: s3://$bucket/metrics is empty after the backup ran" >&2; exit 1; }
echo "hack/backup-restore-proof.sh: PASS — backup objects are on the S3-compatible endpoint"

echo
echo "hack/backup-restore-proof.sh: restoring into a FRESH VictoriaMetrics instance with vmrestore ($vmrestore_image)"
docker volume create "$vol_restored" >/dev/null
docker run --rm --network "$net" \
  -v "$vol_restored:/vm-data" \
  -v "$vol_tools:/var/run/backup-tools:ro" \
  -v "$work/aws-config:/etc/aws/config:ro" \
  -e "AWS_CONFIG_FILE=$aws_config_file" -e "AWS_SDK_LOAD_CONFIG=$sdk_load_config" \
  "$vmrestore_image" \
  -src="s3://$bucket/metrics" \
  -storageDataPath=/vm-data \
  -customS3Endpoint="http://$s3:9090" \
  -s3ForcePathStyle=true

echo
echo "hack/backup-restore-proof.sh: starting the restored VictoriaMetrics and querying it"
docker run -d --name "$restored_vm" --network "$net" \
  -v "$vol_restored:/storage" \
  "victoriametrics/victoria-metrics:$vm_tag" \
  -storageDataPath=/storage -retentionPeriod=100y >/dev/null

docker run --rm --network "$net" "$prober" sh -c "
  for i in \$(seq 1 90); do
    curl -fsS http://$restored_vm:8428/health >/dev/null 2>&1 && exit 0
    sleep 1
  done
  exit 1
" || { echo "hack/backup-restore-proof.sh: restored VictoriaMetrics never came up" >&2; docker logs "$restored_vm" >&2 || true; exit 1; }

restored_val="$(docker run --rm --network "$net" "$prober" sh -c "
  for i in \$(seq 1 90); do
    v=\$(curl -fsS http://$restored_vm:8428/api/v1/query --data-urlencode 'query=proof_backup_restore_marker')
    echo \"\$v\"
    case \"\$v\" in *'\"value\":['*) exit 0 ;; esac
    sleep 1
  done
" | tail -1)"

echo "hack/backup-restore-proof.sh: restored instance query response: $restored_val"
echo "$restored_val" | python3 -c '
import json, sys
d = json.load(sys.stdin)["data"]["result"]
v = d[0]["value"][1] if d else None
if v != "42":
    sys.exit(f"hack/backup-restore-proof.sh: FAIL — restored instance serves {v!r} for proof_backup_restore_marker, not 42")
print("hack/backup-restore-proof.sh: PASS — restore served the seeded series (proof_backup_restore_marker = 42) from a completely fresh instance")
'
