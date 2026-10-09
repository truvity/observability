#!/bin/bash
set -euo pipefail

mkdir -p /etc/statusbox /opt/statusbox/staged /usr/local/sbin

cat > /etc/statusbox/params.sh <<'STATUSBOX_PARAMS'
# Rendered by pkg/statusbox/ec2. Names and pinned checksums only; no secret.
SB_ASG_NAME='statusbox-status'
SB_HOOK_NAME='statusbox-status-launch'
SB_BUCKET='acme-status-replica'
SB_PREFIX='box'
SB_HEALTH_MINUTES='5'
SB_TUNNEL_PARAM='/acme/status/tunnel-token'
SB_PING_PARAM=''
SB_ENV_PARAMS=('ALERT_URL_OPS_ALERTS_READ_TOKEN=/acme/status/alerts-read-token' 'OIDC_CLIENT_SECRET=/acme/status/oidc-client-secret')
SB_INSTANCES=('ops:8082:true:ops.db' 'ops-breakglass:8081:false:ops.db')
SB_CONFIGS=('ops:a68760ad960944a7e40622edc1436c67bf87cab60b1a306e9939ac26e3a97953:box/config/a68760ad960944a7e40622edc1436c67bf87cab60b1a306e9939ac26e3a97953.yaml' 'ops-breakglass:a68760ad960944a7e40622edc1436c67bf87cab60b1a306e9939ac26e3a97953:box/config/a68760ad960944a7e40622edc1436c67bf87cab60b1a306e9939ac26e3a97953.yaml')
SB_SETUP_KEY='box/config/4e857f473790c44a9fd4843e294bb011572b597336861ee5448dc97843374420.sh'
SB_SETUP_SHA='4e857f473790c44a9fd4843e294bb011572b597336861ee5448dc97843374420'
SB_GATUS_URL_arm64='https://github.com/truvity/observability/releases/download/v1.0.0/gatus_v5.37.0_linux_arm64'
SB_GATUS_SHA_arm64='0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef'
SB_LITESTREAM_URL_arm64='https://github.com/benbjohnson/litestream/releases/download/v0.5.17/litestream-0.5.17-linux-arm64.tar.gz'
SB_LITESTREAM_SHA_arm64='f8ca4a050095c1efbda2c4365172e61bf9d955ea0d9ac42f448b52e51819baa5'
SB_CLOUDFLARED_URL_arm64='https://github.com/cloudflare/cloudflared/releases/download/2026.10.0/cloudflared-linux-arm64'
SB_CLOUDFLARED_SHA_arm64='e6422b9d4f72d3194bc5a38676f13667c06666523217b842a877d72a80b5ac08'
SB_GATUS_URL_amd64='https://github.com/truvity/observability/releases/download/v1.0.0/gatus_v5.37.0_linux_amd64'
SB_GATUS_SHA_amd64='abababababababababababababababababababababababababababababababab'
SB_LITESTREAM_URL_amd64='https://github.com/benbjohnson/litestream/releases/download/v0.5.17/litestream-0.5.17-linux-x86_64.tar.gz'
SB_LITESTREAM_SHA_amd64='cfb371176d164437ae869f8351cfde49bd1804ae71c61923f75c9cba9c9c006d'
SB_CLOUDFLARED_URL_amd64='https://github.com/cloudflare/cloudflared/releases/download/2026.10.0/cloudflared-linux-amd64'
SB_CLOUDFLARED_SHA_amd64='d33ff2d14475178d2012c2c56beba87389ac5ded27649519f198a7d3134a99db'
STATUSBOX_PARAMS

# Fetch the setup script: it is an S3 object, not user-data (see
# configobjects.go), pinned here by its sha256. Anything but an exact match,
# after the retries, abandons the launch: the group replaces the instance and
# nothing unverified ever runs.
. /etc/statusbox/params.sh
tok="$(curl -fsS -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' http://169.254.169.254/latest/api/token)"
imds() { curl -fsS -H "X-aws-ec2-metadata-token: $tok" "http://169.254.169.254/latest/$1"; }
AWS_REGION="$(imds meta-data/placement/region)"
export AWS_REGION
dest=/usr/local/sbin/statusbox-setup
for i in 1 2 3 4 5; do
  if aws s3 cp --only-show-errors "s3://$SB_BUCKET/$SB_SETUP_KEY" "$dest.part" && [ "$(sha256sum "$dest.part" | cut -d' ' -f1)" = "$SB_SETUP_SHA" ]; then
    chmod 0755 "$dest.part" && mv "$dest.part" "$dest"
    break
  fi
  rm -f "$dest.part"
  if [ "$i" = 5 ]; then
    echo "statusbox: the setup script could not be fetched and verified; abandoning the launch" >&2
    aws autoscaling complete-lifecycle-action --lifecycle-hook-name "$SB_HOOK_NAME" --auto-scaling-group-name "$SB_ASG_NAME" \
      --instance-id "$(imds meta-data/instance-id)" --lifecycle-action-result ABANDON || true
    exit 1
  fi
  sleep 5
done
exec /usr/local/sbin/statusbox-setup install
