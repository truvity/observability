#!/usr/bin/env bash
# statusbox-self-register: points a DNS A record at this instance's private IP.
#
# It is run by statusbox-boot.service as an ExecStartPost, that is AFTER the boot
# phase has completed the Auto Scaling lifecycle hook, so it can never delay or
# prevent InService. It is fail-safe: every failure is logged and the script
# exits 0. It does nothing unless the instance's target lifecycle state is
# InService, so a warm-pool instance that is only being pre-warmed never
# registers; the one that takes over registers on its own boot.
#
# It reads the private IP from IMDSv2, assumes the cross-account role named in
# /etc/statusbox/params.sh (SB_DNS_ROLE_ARN) and UPSERTs the A record
# SB_DNS_RECORD in the hosted zone SB_DNS_ZONE_ID with TTL SB_DNS_TTL. The role
# is limited to that one record name. Nothing secret is written to disk or to
# the log: the temporary credentials live in this process's environment only.
set -uo pipefail

PARAMS="${STATUSBOX_PARAMS:-/etc/statusbox/params.sh}"
ATTEMPTS=3

log() { printf '%s statusbox-self-register: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*" >&2; }

imds_token() {
  curl -fsS -m 5 -X PUT "http://169.254.169.254/latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 300"
}

imds() {
  curl -fsS -m 5 -H "X-aws-ec2-metadata-token: $(imds_token)" "http://169.254.169.254/latest/$1"
}

# register_once prints nothing on success and returns non-zero on any failure.
register_once() {
  local ip="$1" region creds ak sk st batch
  region="$(imds meta-data/placement/region)" || return 1
  creds="$(AWS_REGION="$region" aws sts assume-role \
    --role-arn "$SB_DNS_ROLE_ARN" \
    --role-session-name statusbox-self-register \
    --duration-seconds 900 \
    --query 'Credentials.[AccessKeyId,SecretAccessKey,SessionToken]' \
    --output text)" || return 1
  read -r ak sk st <<<"$creds"
  [ -n "$ak" ] && [ -n "$sk" ] && [ -n "$st" ] || return 1
  batch="{\"Comment\":\"statusbox self-register\",\"Changes\":[{\"Action\":\"UPSERT\",\"ResourceRecordSet\":{\"Name\":\"$SB_DNS_RECORD\",\"Type\":\"A\",\"TTL\":$SB_DNS_TTL,\"ResourceRecords\":[{\"Value\":\"$ip\"}]}}]}"
  AWS_REGION="$region" AWS_ACCESS_KEY_ID="$ak" AWS_SECRET_ACCESS_KEY="$sk" AWS_SESSION_TOKEN="$st" \
    aws route53 change-resource-record-sets \
    --hosted-zone-id "$SB_DNS_ZONE_ID" \
    --change-batch "$batch" \
    --query 'ChangeInfo.Id' --output text >/dev/null
}

main() {
  local state ip n
  # shellcheck disable=SC1090
  . "$PARAMS" || { log "no params; nothing to do"; return 0; }
  if [ -z "${SB_DNS_ROLE_ARN:-}" ] || [ -z "${SB_DNS_ZONE_ID:-}" ] || [ -z "${SB_DNS_RECORD:-}" ]; then
    log "not configured; nothing to do"
    return 0
  fi
  SB_DNS_TTL="${SB_DNS_TTL:-60}"

  state="$(imds meta-data/autoscaling/target-lifecycle-state 2>/dev/null || true)"
  if [ "$state" != "InService" ]; then
    log "target lifecycle state '${state:-none}', not InService; not registering"
    return 0
  fi

  ip="$(imds meta-data/local-ipv4)" || { log "cannot read the private IP from IMDS"; return 0; }
  if ! [[ "$ip" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]]; then
    log "IMDS returned something that is not an IPv4 address; not registering"
    return 0
  fi

  for n in $(seq 1 "$ATTEMPTS"); do
    if register_once "$ip"; then
      log "registered $SB_DNS_RECORD -> $ip (ttl $SB_DNS_TTL)"
      return 0
    fi
    log "attempt $n/$ATTEMPTS failed"
    [ "$n" = "$ATTEMPTS" ] || sleep 5
  done
  log "giving up after $ATTEMPTS attempts; $SB_DNS_RECORD is unchanged"
  return 0
}

# Sourced for a test (STATUSBOX_SOURCE_ONLY=1) the functions are defined and
# nothing runs.
if [ -z "${STATUSBOX_SOURCE_ONLY:-}" ]; then
  main "$@"
fi
