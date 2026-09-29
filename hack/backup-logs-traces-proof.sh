#!/usr/bin/env bash
# hack/backup-logs-traces-proof.sh — REAL proof, in Docker, of the two
# two defects in the logs/traces backup CronJobs, fixed, against the
# PINNED, UNMODIFIED `backup.image` (rclone/rclone:1.73.0) and real
# `victoria-logs`/`victoria-traces` binaries at the versions
# `charts/observability-stack`'s own vendored subcharts pin:
#
#   1. Store auth is mandatory, and busybox wget (this image's only HTTP
#      client) has no `--user`/`--password` at all — confirmed directly
#      against this image, 2026-09-29 (`wget: unrecognized option`). The
#      fix builds an RFC 7617 `Authorization: Basic` header by hand
#      (`--header`, which busybox DOES implement) instead.
#   2. rclone reads an `s3://bucket/...` destination as a remote named
#      "s3" that does not exist ("didn't find section in config file")
#      and does nothing, successfully — confirmed directly against this
#      image. The fix translates `backup.destination` into rclone's own
#      connection-string syntax (`observability-stack.backup.
#      rcloneDestination` in templates/_helpers.tpl) before it reaches
#      the logs/traces jobs.
#
# This script does not hand-type either command. It renders the REAL
# chart with `helm template` (`backup.auth.mode: secret`, both stores
# pointed at containers this script starts via `stores.logs.url`/
# `stores.traces.url`) and parses the `backup-logs`/`backup-traces`
# CronJobs' own `command`/`env`/`image` back out of that output — the
# same extract-and-run shape as hack/backup-restore-proof.sh. The only
# hand-resolved pieces are `VM_USERNAME`/`VM_PASSWORD` (a real Secret's
# env would resolve them in a cluster; here this script's own store
# credentials fill the same env vars by hand) and the S3-compatible
# credentials/endpoint (`env_auth=true`, rclone's OWN instruction to
# resolve them from the environment — this script supplies that
# environment, an EKS Pod Identity/IRSA role would in a real `ambient`
# install). Every other rendered token — the wget/rclone command lines
# themselves, `DESTINATION`, `STORE_URL` — runs completely unmodified.
#
# adobe/s3mock stands in for the S3-compatible object store, the same
# substitution hack/backup-restore-proof.sh already uses and for the
# same reason: `minio/minio`'s Docker Hub image now sits behind a
# subscription wall (`pull access denied`, checked 2026-09-29;
# `quay.io/minio/minio` answers 401 the same way) — not available to
# pull in this repository's CI, or on a laptop with no MinIO account.
# S3Mock is a real, if partial, S3-compatible HTTP server (not a stub of
# rclone or wget), and a `docker run ... amazon/aws-cli ... s3 ls`
# against it after each job is the actual proof the object landed.
#
# Every HTTP interaction runs from a throwaway curlimages/curl container
# ON THE SAME DOCKER NETWORK as its target, addressed by container name —
# see hack/backup-restore-proof.sh's own header for why (host-published
# ports proved unreliable on the box this was developed on).
#
# Needs Docker and python3 (PyYAML). Deliberately NOT part of `check`: a
# one-off, run-by-hand proof, the same reason
# hack/backup-restore-proof.sh's own Docker requirement is not — the
# golden renders (tests/golden/observability-stack/backup-*.yaml) and
# tests/vendored_sync_sources_test.go are the regression gate.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d -t backup-logs-traces-proof.XXXXXX)"

net=backup-lt-proof-net
s3=backup-lt-proof-s3
logs_store=backup-lt-proof-vlogs
traces_store=backup-lt-proof-vtraces
prober=curlimages/curl:latest
bucket=backup-lt-proof
vm_user=proofuser
vm_pass='pr##f/p@ss=word'
s3_key=proofs3key
s3_secret=proofs3secretkey12345

vol_logs=backup-lt-proof-logs-data
vol_traces=backup-lt-proof-traces-data

cleanup() {
  docker rm -f "$s3" "$logs_store" "$traces_store" >/dev/null 2>&1 || true
  docker volume rm -f "$vol_logs" "$vol_traces" >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

for tool in docker python3; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "hack/backup-logs-traces-proof.sh needs $tool, which is not on PATH" >&2
    exit 1
  }
done
python3 -c 'import yaml' 2>/dev/null || {
  echo "hack/backup-logs-traces-proof.sh needs python3's PyYAML (import yaml)" >&2
  exit 1
}

echo "hack/backup-logs-traces-proof.sh: rendering charts/observability-stack with backup.logs/traces.enabled against real store addresses"
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
stores:
  logs:
    url: "http://$logs_store:9428"
  traces:
    url: "http://$traces_store:10428"
backup:
  enabled: true
  destination: "s3://$bucket/observability"
  auth:
    mode: secret
  credentialsSecret: example-backup-credentials
  metrics:
    enabled: false
  logs:
    enabled: true
  traces:
    enabled: true
EOF

helm template proof "$root/charts/observability-stack" --values "$work/values.yaml" > "$work/rendered.yaml"

cat > "$work/extract.py" <<'PYEOF'
import sys, yaml, json

rendered = open(sys.argv[1]).read()
docs = [d for d in yaml.safe_load_all(rendered) if d]

out = {}
for store, suffix in (("logs", "backup-logs"), ("traces", "backup-traces")):
    cronjob = next(d for d in docs if d.get("kind") == "CronJob" and d["metadata"]["name"].endswith(suffix))
    pod = cronjob["spec"]["jobTemplate"]["spec"]["template"]["spec"]
    container = pod["containers"][0]
    out[store] = {
        "image": container["image"],
        "command": container["command"],
        "env": {e["name"]: e.get("value", "") for e in container["env"] if "value" in e},
    }
json.dump(out, sys.stdout)
PYEOF
extracted_json="$(python3 "$work/extract.py" "$work/rendered.yaml")"
echo "hack/backup-logs-traces-proof.sh: extracted from the RENDERED CronJobs (this is the chart's own output, not hand-written):"
echo "$extracted_json" | python3 -m json.tool

backup_image="$(echo "$extracted_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["logs"]["image"])')"
echo "hack/backup-logs-traces-proof.sh: backup.image the chart pins: $backup_image"
[ "$backup_image" = "rclone/rclone:1.73.0" ] || {
  echo "hack/backup-logs-traces-proof.sh: rendered backup.image is $backup_image, this proof was written against rclone/rclone:1.73.0 — update the image tags in the proof (and re-check the wget/base64 flags this whole script exists to confirm) before trusting a PASS" >&2
  exit 1
}

logs_destination="$(echo "$extracted_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["logs"]["env"]["DESTINATION"])')"
traces_destination="$(echo "$extracted_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["traces"]["env"]["DESTINATION"])')"
logs_store_url="$(echo "$extracted_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["logs"]["env"]["STORE_URL"])')"
traces_store_url="$(echo "$extracted_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["traces"]["env"]["STORE_URL"])')"
echo "hack/backup-logs-traces-proof.sh: rendered DESTINATION (logs):   $logs_destination"
echo "hack/backup-logs-traces-proof.sh: rendered DESTINATION (traces): $traces_destination"
case "$logs_destination" in
  :s3,env_auth=true:*) ;;
  *) echo "hack/backup-logs-traces-proof.sh: DESTINATION does not look like a translated rclone connection string — the fix this proof exists to confirm did not render" >&2; exit 1 ;;
esac

mapfile -d '' -t logs_command < <(echo "$extracted_json" | python3 -c '
import json, sys
for c in json.load(sys.stdin)["logs"]["command"]:
    sys.stdout.write(c)
    sys.stdout.write("\0")
')
mapfile -d '' -t traces_command < <(echo "$extracted_json" | python3 -c '
import json, sys
for c in json.load(sys.stdin)["traces"]["command"]:
    sys.stdout.write(c)
    sys.stdout.write("\0")
')

netcurl() { docker run --rm --network "$net" "$prober" curl "$@"; }

echo
echo "hack/backup-logs-traces-proof.sh: creating the isolated network"
docker network create "$net" >/dev/null
docker pull -q "$prober" >/dev/null

echo "hack/backup-logs-traces-proof.sh: starting the S3-compatible endpoint (adobe/s3mock — see this script's own header for why not minio/minio)"
docker run -d --name "$s3" --network "$net" \
  -e COM_ADOBE_TESTING_S3MOCK_STORE_INITIAL_BUCKETS="$bucket" \
  adobe/s3mock:latest >/dev/null
docker run --rm --network "$net" "$prober" sh -c "
  for i in \$(seq 1 90); do
    code=\$(curl -s -o /dev/null -w '%{http_code}' http://$s3:9090/$bucket)
    [ \"\$code\" = 200 ] && exit 0
    sleep 1
  done
  exit 1
" || { echo "hack/backup-logs-traces-proof.sh: S3-compatible endpoint never became healthy" >&2; docker logs "$s3" >&2 || true; exit 1; }
echo "hack/backup-logs-traces-proof.sh: S3-compatible endpoint healthy, bucket s3://$bucket exists"

echo
echo "hack/backup-logs-traces-proof.sh: starting a real victoria-logs, store auth ON (-httpAuth.*), the exact image charts/observability-stack's victoria-logs-single dependency pins"
# `/storage`, not a made-up path: the vendored victoria-logs-single chart
# mounts the store's own volume there (values.yaml's mountPath), and
# `templates/backup.yaml` mounts the SAME PVC at the SAME path into the
# backup job — the snapshot API returns a path under -storageDataPath,
# and the backup job's `test -d "$path"` only finds it because both
# containers agree on where the volume lives.
docker volume create "$vol_logs" >/dev/null
docker run -d --name "$logs_store" --network "$net" \
  -v "$vol_logs:/storage" \
  victoriametrics/victoria-logs:v1.52.0 \
  -storageDataPath=/storage -retentionPeriod=100y \
  -httpAuth.username="$vm_user" -httpAuth.password="$vm_pass" >/dev/null
docker run --rm --network "$net" "$prober" sh -c "
  for i in \$(seq 1 90); do
    curl -fsS http://$logs_store:9428/health >/dev/null 2>&1 && exit 0
    sleep 1
  done
  exit 1
" || { echo "hack/backup-logs-traces-proof.sh: victoria-logs never came up" >&2; docker logs "$logs_store" >&2 || true; exit 1; }

echo "hack/backup-logs-traces-proof.sh: seeding one log line, with basic auth on, so the snapshot has a partition to copy"
netcurl -fsS -u "$vm_user:$vm_pass" -X POST "http://$logs_store:9428/insert/jsonline?_msg_field=message&_time_field=time" \
  -H 'Content-Type: application/stream+json' \
  --data-binary "{\"time\":\"$(date -u +%FT%TZ)\",\"message\":\"backup-logs-traces-proof marker\"}" >/dev/null
netcurl -fsS "http://$logs_store:9428/internal/force_flush" >/dev/null || true
sleep 2

echo
echo "hack/backup-logs-traces-proof.sh: starting a real victoria-traces, store auth ON, the exact image charts/observability-stack's victoria-traces-single dependency pins"
docker volume create "$vol_traces" >/dev/null
docker run -d --name "$traces_store" --network "$net" \
  -v "$vol_traces:/storage" \
  victoriametrics/victoria-traces:v0.11.0 \
  -storageDataPath=/storage -retentionPeriod=100y \
  -httpAuth.username="$vm_user" -httpAuth.password="$vm_pass" >/dev/null
docker run --rm --network "$net" "$prober" sh -c "
  for i in \$(seq 1 90); do
    curl -fsS http://$traces_store:10428/health >/dev/null 2>&1 && exit 0
    sleep 1
  done
  exit 1
" || { echo "hack/backup-logs-traces-proof.sh: victoria-traces never came up" >&2; docker logs "$traces_store" >&2 || true; exit 1; }

echo "hack/backup-logs-traces-proof.sh: seeding one span, with basic auth on, so the trace store has a partition to copy"
otlp_span='{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"backup-logs-traces-proof"}}]},"scopeSpans":[{"spans":[{"traceId":"5B8EFFF798038103D269B633813FC60C","spanId":"EEE19B7EC3C1B174","name":"proof-span","startTimeUnixNano":"1700000000000000000","endTimeUnixNano":"1700000000100000000"}]}]}]}'
netcurl -fsS -u "$vm_user:$vm_pass" -X POST "http://$traces_store:10428/insert/opentelemetry/v1/traces" \
  -H 'Content-Type: application/json' --data-binary "$otlp_span" >/dev/null

echo
echo "=================================================================="
echo "hack/backup-logs-traces-proof.sh: DEFECT 1 — running the RENDERED logs backup command (busybox wget + rclone, auth ON) against real victoria-logs + the S3-compatible endpoint:"
echo "=================================================================="
printf '  %s\n' "${logs_command[@]}"
docker run --rm --network "$net" \
  -v "$vol_logs:/storage:ro" \
  -e STORE_URL="$logs_store_url" \
  -e DESTINATION="$logs_destination" \
  -e VM_USERNAME="$vm_user" -e VM_PASSWORD="$vm_pass" \
  -e AWS_ACCESS_KEY_ID="$s3_key" -e AWS_SECRET_ACCESS_KEY="$s3_secret" \
  -e RCLONE_S3_ENDPOINT="http://$s3:9090" -e RCLONE_S3_FORCE_PATH_STYLE=true \
  --entrypoint "${logs_command[0]}" \
  "$backup_image" "${logs_command[@]:1}"

echo
echo "hack/backup-logs-traces-proof.sh: confirming the logs backup landed on the S3-compatible endpoint"
logs_listing="$(docker run --rm --network "$net" \
  -e AWS_ACCESS_KEY_ID="$s3_key" -e AWS_SECRET_ACCESS_KEY="$s3_secret" -e AWS_DEFAULT_REGION=us-east-1 \
  amazon/aws-cli --endpoint-url "http://$s3:9090" s3 ls "s3://$bucket/observability/logs" --recursive)"
echo "$logs_listing"
[ -n "$logs_listing" ] || { echo "hack/backup-logs-traces-proof.sh: s3://$bucket/observability/logs is empty after the backup ran" >&2; exit 1; }
echo "hack/backup-logs-traces-proof.sh: PASS — logs snapshot created (busybox wget + Authorization: Basic header, auth ON) and uploaded (rclone, translated destination)"

echo
echo "=================================================================="
echo "hack/backup-logs-traces-proof.sh: DEFECT 1 + 2 — running the RENDERED traces backup command (sync/detach/sync/attach, auth ON) against real victoria-traces + the S3-compatible endpoint:"
echo "=================================================================="
printf '  %s\n' "${traces_command[@]}"
docker run --rm --network "$net" \
  -v "$vol_traces:/storage:ro" \
  -e STORE_URL="$traces_store_url" \
  -e DESTINATION="$traces_destination" \
  -e VM_USERNAME="$vm_user" -e VM_PASSWORD="$vm_pass" \
  -e AWS_ACCESS_KEY_ID="$s3_key" -e AWS_SECRET_ACCESS_KEY="$s3_secret" \
  -e RCLONE_S3_ENDPOINT="http://$s3:9090" -e RCLONE_S3_FORCE_PATH_STYLE=true \
  --entrypoint "${traces_command[0]}" \
  "$backup_image" "${traces_command[@]:1}"

echo
echo "hack/backup-logs-traces-proof.sh: confirming the traces backup landed on the S3-compatible endpoint"
traces_listing="$(docker run --rm --network "$net" \
  -e AWS_ACCESS_KEY_ID="$s3_key" -e AWS_SECRET_ACCESS_KEY="$s3_secret" -e AWS_DEFAULT_REGION=us-east-1 \
  amazon/aws-cli --endpoint-url "http://$s3:9090" s3 ls "s3://$bucket/observability/traces" --recursive)"
echo "$traces_listing"
[ -n "$traces_listing" ] || { echo "hack/backup-logs-traces-proof.sh: s3://$bucket/observability/traces is empty after the backup ran" >&2; exit 1; }
echo "hack/backup-logs-traces-proof.sh: PASS — traces synced/detached/synced/attached (busybox wget + Authorization: Basic header, auth ON) and uploaded (rclone, translated destination)"

echo
echo "hack/backup-logs-traces-proof.sh: ALL PASS — both defects fixed against the pinned, unmodified rclone/rclone:1.73.0 and real victoria-logs/victoria-traces, store auth ON throughout"
