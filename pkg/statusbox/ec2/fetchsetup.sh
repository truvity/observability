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
