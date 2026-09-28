#!/usr/bin/env bash
# hack/statusbox-ca-proof.sh — REAL proof, in Docker, that
# statusbox.Args.TrustedCAs / setup.sh's setup_trusted_cas /
# write_compose's SSL_CERT_DIR wiring actually makes Gatus trust a
# private root, against the real twinproduction/gatus:v5.37.0 image —
# not a stand-in. hack/statusbox-ci.sh already proves setup.sh's OWN
# logic (the staged file lands under $trusted_ca_dir, the compose file
# gets the mount and the env line); this script is the other half: does
# Gatus's own Go TLS stack actually behave differently because of it.
#
# Three things, in ONE run, so the second and third can't be argued to
# be a coincidence of test ordering or a fluke of the fixture:
#
#   1. WITHOUT the CA mounted, a probe of an HTTPS endpoint whose
#      certificate is signed by a throwaway private root FAILS with
#      exactly the error this feature exists to fix:
#        tls: failed to verify certificate: x509: certificate signed by
#        unknown authority
#   2. WITH SSL_CERT_DIR pointed at a mounted directory holding that
#      root, the SAME probe SUCCEEDS.
#   3. In that SAME with-CA container, an ordinary public HTTPS probe
#      (https://example.com) ALSO succeeds — proving SSL_CERT_DIR adds
#      to the image's own public trust bundle rather than replacing it,
#      exactly as write_compose's own doc comment in setup.sh claims
#      from reading crypto/x509/root.go and the upstream Gatus
#      Dockerfile.
#
# The private HTTPS server is reached by a DNS name
# (private.statusbox-ca-proof.test), the same way a real Config
# addresses a private service — resolved here through --add-host rather
# than a second, freshly-created docker network's own embedded DNS,
# because some Docker hosts restrict a NEW bridge network's own egress
# more tightly than the default one, and what this script exists to
# prove (does Gatus's OWN TLS stack trust the mounted root, and does it
# still trust the public web) does not depend on which mechanism handed
# it the name — only on what SSL_CERT_DIR does once the connection is
# open.
#
# Needs Docker, openssl, curl and jq. Deliberately not part of `check`
# or CI, the same reason hack/statusbox-debian-ci.sh's own Docker
# requirement is not: a one-off, run-by-hand proof for this feature,
# not a regression gate.
set -euo pipefail

work="$(mktemp -d -t statusbox-ca-proof.XXXXXX)"
priv_host="private.statusbox-ca-proof.test"

server=statusbox-ca-proof-server
with_ca=statusbox-ca-proof-with-ca
no_ca=statusbox-ca-proof-no-ca

cleanup() {
  docker rm -f "$server" "$with_ca" "$no_ca" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

for tool in docker openssl curl jq; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "hack/statusbox-ca-proof.sh needs $tool, which is not on PATH" >&2
    exit 1
  }
done

echo "hack/statusbox-ca-proof.sh: generating a throwaway root CA and a server certificate for $priv_host"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
  -keyout "$work/ca.key" -out "$work/ca.pem" -days 2 \
  -subj "/CN=statusbox-ca-proof-root" >/dev/null 2>&1
openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
  -keyout "$work/server.key" -out "$work/server.csr" \
  -subj "/CN=$priv_host" >/dev/null 2>&1
openssl x509 -req -in "$work/server.csr" -CA "$work/ca.pem" -CAkey "$work/ca.key" -CAcreateserial \
  -out "$work/server.pem" -days 2 \
  -extfile <(printf 'subjectAltName=DNS:%s' "$priv_host") >/dev/null 2>&1

# $work/ca-dir is the fixture's stand-in for setup_trusted_cas's own
# $trusted_ca_dir: one file, extra-roots.pem, in an otherwise empty
# directory — exactly what a real box would have unpacked there.
mkdir -p "$work/ca-dir"
cp "$work/ca.pem" "$work/ca-dir/extra-roots.pem"

echo "hack/statusbox-ca-proof.sh: starting the private HTTPS server (openssl s_server, -www mode)"
docker run -d --name "$server" \
  -v "$work:/certs:ro" \
  alpine/openssl s_server -quiet -www -cert /certs/server.pem -key /certs/server.key -accept 8443 >/dev/null

sleep 2
server_ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$server")"
echo "hack/statusbox-ca-proof.sh: $priv_host -> $server_ip (via --add-host on the gatus containers below)"

cat > "$work/gatus-config.yaml" <<EOF
endpoints:
  - name: private-endpoint
    url: "https://${priv_host}:8443/"
    interval: 5s
    conditions:
      - "[STATUS] == 200"
  - name: public-endpoint
    url: "https://example.com"
    interval: 5s
    conditions:
      - "[STATUS] == 200"
EOF

echo "hack/statusbox-ca-proof.sh: starting gatus WITH the CA directory mounted and SSL_CERT_DIR set — the shape write_compose renders"
docker run -d --name "$with_ca" \
  --add-host "$priv_host:$server_ip" \
  -p 127.0.0.1::8080 \
  -e SSL_CERT_DIR=/etc/ssl/extra-ca \
  -e GATUS_CONFIG_PATH=/config/config.yaml \
  -v "$work/gatus-config.yaml:/config/config.yaml:ro" \
  -v "$work/ca-dir:/etc/ssl/extra-ca:ro" \
  twinproduction/gatus:v5.37.0 >/dev/null

echo "hack/statusbox-ca-proof.sh: starting gatus WITHOUT the CA mounted — the failure this feature exists to fix"
docker run -d --name "$no_ca" \
  --add-host "$priv_host:$server_ip" \
  -p 127.0.0.1::8080 \
  -e GATUS_CONFIG_PATH=/config/config.yaml \
  -v "$work/gatus-config.yaml:/config/config.yaml:ro" \
  twinproduction/gatus:v5.37.0 >/dev/null

with_ca_port="$(docker port "$with_ca" 8080/tcp | cut -d: -f2)"
no_ca_port="$(docker port "$no_ca" 8080/tcp | cut -d: -f2)"

echo "hack/statusbox-ca-proof.sh: waiting for both to evaluate at least once (5s interval)"
sleep 10

status_json() { # host port, endpoint key -> the latest result, as JSON
  curl -fsS "http://127.0.0.1:$1/api/v1/endpoints/$2/statuses" | jq '.results[-1]'
}

with_private="$(status_json "$with_ca_port" _private-endpoint)"
with_public="$(status_json "$with_ca_port" _public-endpoint)"
no_private="$(status_json "$no_ca_port" _private-endpoint)"
no_public="$(status_json "$no_ca_port" _public-endpoint)"

with_private_ok="$(jq -r '.success' <<<"$with_private")"
with_public_ok="$(jq -r '.success' <<<"$with_public")"
no_private_ok="$(jq -r '.success' <<<"$no_private")"
no_private_err="$(jq -r '.errors[0] // empty' <<<"$no_private")"
no_public_ok="$(jq -r '.success' <<<"$no_public")"

echo
echo "=== hack/statusbox-ca-proof.sh: results ==="
echo "WITH the CA mounted + SSL_CERT_DIR=/etc/ssl/extra-ca:"
echo "  private endpoint (signed by the throwaway private root): success=$with_private_ok"
echo "  public endpoint  (https://example.com):                  success=$with_public_ok"
echo "WITHOUT the mount:"
echo "  private endpoint (signed by the throwaway private root): success=$no_private_ok"
echo "    error: $no_private_err"
echo "  public endpoint  (https://example.com):                  success=$no_public_ok"
echo

fail=0

[ "$with_private_ok" = "true" ] || {
  echo "FAIL: the private endpoint did NOT succeed with the CA mounted and SSL_CERT_DIR set" >&2
  fail=1
}
[ "$with_public_ok" = "true" ] || {
  echo "FAIL: the public endpoint did NOT succeed with the CA mounted — SSL_CERT_DIR must be ADDITIVE to the image's own public bundle, not a replacement of it" >&2
  fail=1
}
[ "$no_private_ok" = "false" ] || {
  echo "FAIL: the private endpoint succeeded even WITHOUT the CA mounted — this fixture is not actually proving anything (the server may not be presenting the private-root certificate at all)" >&2
  fail=1
}
case "$no_private_err" in
*"certificate signed by unknown authority"*) ;;
*)
  echo "FAIL: without the CA mounted, the private endpoint's error was not the expected x509 one. Got: \"$no_private_err\"" >&2
  fail=1
  ;;
esac
[ "$no_public_ok" = "true" ] || {
  echo "FAIL: the public endpoint did not succeed even without the CA mounted — something else in this fixture is broken (network egress, DNS, ...), independent of statusbox's own change" >&2
  fail=1
}

if [ "$fail" = 0 ]; then
  echo "hack/statusbox-ca-proof.sh: SSL_CERT_DIR trusts the private root, keeps the public bundle trusted, and its absence reproduces the exact x509 failure — OK"
fi
exit $fail
