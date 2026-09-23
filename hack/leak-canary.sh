#!/usr/bin/env bash
# This repository is public and its history cannot be unpublished — a
# rewrite changes the SHAs but not what was already fetched. So the rule
# ("mechanism only; particulars are caller inputs or org variables") is
# enforced mechanically rather than remembered.
#
# Vendored from truvity/ci-workflows (hack/leak-canary.sh), which is public
# for the same reason. Keep it in step with that copy.
#
# ONE DELTA from that copy, stated here because the contract requires a
# narrowing to be explained rather than quietly applied. The `/secrets/`
# pattern exists to catch a parameter-store path, and it also matches the
# fixed address at which Kubernetes projects a pod's own service account
# token: `/var/run/secrets/kubernetes.io/serviceaccount`. That address is
# mechanism, not a particular — every pod in every cluster has it, it
# names nothing about any estate, and charts/observability-emitters
# renders it into the metrics agent's inline scrape configs so the agent
# can authenticate to the kubelet as itself. It is excluded by its exact
# literal below; the pattern it was matching still catches every real
# parameter-store path, including one that happens to sit under
# /var/run. The cost of the exclusion is that a line carrying BOTH this
# address and a genuine particular would be missed, which is why the
# exclusion is an exact string and not a prefix.
#
# Every chart value or module input that names a cluster, an account, a
# hostname or a secret path is an INPUT with a neutral default; the
# consuming estate supplies the particulars from its own repository.
#
# Add a pattern here the first time something new turns out to be a
# particular. Never add an exception without one.
set -uo pipefail

# The 12-digit patterns are anchored on word boundaries. Without them,
# `[0-9]{12}` also matches a 12-digit run that happens to fall inside a
# longer hex string -- and a nixpkgs commit SHA is exactly that. The
# devbox bump to 17de0b976395537756f30a3e78f2f06e5cec89ed contains
# `976395537756`, which failed this canary simultaneously in every repo
# that carries it, for a value that is neither a particular nor secret.
# `\b` keeps every real shape (bare, in an ARN, as an ECR host: each is
# bounded by a non-word character) and drops the hex-embedded ones.
patterns=(
  '\b[0-9]{12}\b'                          # AWS account id
  'arn:aws'                            # any ARN
  '\b[0-9]{12}\.dkr\.ecr\.'              # ECR registry host
  '\.svc\.cluster\.local'              # in-cluster DNS
  '/secrets/'                          # SSM parameter paths
  'truvity-[a-z0-9-]*-(ci-cache|artifacts|state)'   # S3 buckets
  '\.truvity\.(xyz|com|co)'            # internal hostnames
  'glpat-|ghp_|github_pat_'            # tokens, in case of an accident
)

# Mechanism that the patterns above necessarily match. See the delta note
# in the header: every entry here is an address that is identical in every
# cluster and names nothing about any estate.
mechanism='/var/run/secrets/kubernetes\.io/serviceaccount'

fail=0

# Scan TRACKED FILES ONLY. The point of this canary is to stop particulars
# being committed, so git's index is exactly the right scope -- and a
# recursive walk of the working tree is not. It descended into generated,
# gitignored directories: .devbox/state.json carries a
# `nix_print_dev_env_hash` whose hex contains a 12-digit run, which matched
# the AWS-account-id pattern. That made the canary fail on a clean checkout
# for a value that is neither committed nor secret.
#
# This matters more than a nuisance: a canary that cries wolf is one people
# learn to skip, and this one is what stands between us and publishing
# particulars from a public repo.
mapfile -d '' tracked < <(git ls-files -z)

for p in "${patterns[@]}"; do
  # Exclude this script: it necessarily contains the patterns it bans.
  if hits=$(printf '%s\0' "${tracked[@]}" \
              | grep -zZv '^hack/leak-canary\.sh$' \
              | xargs -0 -r grep -InE "$p" 2>/dev/null \
              | grep -vE "$mechanism"); then
    echo "LEAK: pattern /$p/ matched — particulars belong in caller inputs or org variables:"
    echo "$hits" | head -5 | sed 's/^/    /'
    fail=1
  fi
done

if [ "$fail" = 0 ]; then
  echo "leak canary clean — ${#patterns[@]} patterns checked, no particulars found"
fi
exit $fail
