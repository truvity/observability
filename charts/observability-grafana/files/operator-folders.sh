set -u
base="${GRAFANA_URL%/}"
auth=/work/auth
# The credentials live in a curl config file in memory, never in argv or logs.
umask 077
printf 'user = "%s:%s"\n' "$GRAFANA_ADMIN_USER" "$GRAFANA_ADMIN_PASSWORD" > "$auth"
log() { printf '%s operator-folders: %s\n' "$(date -u +%FT%TZ)" "$*"; }
failed=0
# call METHOD PATH [JSON]: sets $code and $body; a body is never printed.
call() {
  if [ $# -ge 3 ]; then
    out=$(curl -sS --max-time 20 -K "$auth" -X "$1" -H 'Content-Type: application/json' -w '\n%{http_code}' --data "$3" "$base$2" 2>/dev/null) || out=$(printf '\n000')
  else
    out=$(curl -sS --max-time 20 -K "$auth" -X "$1" -w '\n%{http_code}' "$base$2" 2>/dev/null) || out=$(printf '\n000')
  fi
  code=$(printf '%s\n' "$out" | tail -n 1)
  body=$(printf '%s\n' "$out" | sed '$d')
}
need() { # need "<what>" <ok codes...>
  what="$1"; shift
  for ok in "$@"; do [ "$code" = "$ok" ] && return 0; done
  log "$what failed (http status $code)"; failed=1; return 1
}
idof() { printf '%s' "$body" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p' | head -n 1; }

for i in 1 2 3 4 5 6 7 8 9 10 11 12; do
  call GET /api/health
  [ "$code" = 200 ] && break
  sleep 10
done
need "grafana health" 200 || exit 1

for c in $CLUSTERS; do
  login="workload:${c}${SUBJECT_SUFFIX}"
  enc=$(printf '%s' "$login" | sed 's/:/%3A/g')

  call GET "/api/folders/$c"
  if [ "$code" = 404 ]; then
    call POST /api/folders "{\"uid\":\"$c\",\"title\":\"$c\"}"
    need "create folder $c" 200 || continue
    log "folder $c created"
  else
    need "read folder $c" 200 || continue
  fi

  call GET "/api/users/lookup?loginOrEmail=$enc"
  if [ "$code" = 404 ]; then
    pw=$(head -c 24 /dev/urandom | base64 | tr -d '\n')
    call POST /api/admin/users "{\"login\":\"$login\",\"name\":\"grafana-operator $c\",\"password\":\"$pw\"}"
    pw=
    need "create operator user for $c" 200 || continue
    log "operator user for $c created"
    call GET "/api/users/lookup?loginOrEmail=$enc"
  fi
  need "look up operator user for $c" 200 || continue
  uid=$(idof)
  [ -n "$uid" ] || { log "operator user for $c has no id"; failed=1; continue; }

  call PATCH "/api/org/users/$uid" '{"role":"None"}'
  need "set role None for $c" 200 || continue
  call POST "/api/access-control/folders/$c/users/$uid" '{"permission":"Edit"}'
  need "grant Edit on $c" 200 || continue
  log "cluster $c reconciled"
done

if [ "${PRUNE:-false}" = true ]; then
  call GET "/api/users/search?query=workload%3A&perpage=1000"
  if need "list operator users" 200; then
    printf '%s\n' "$body" | sed 's/{"id"/\n{"id"/g' | sed -n 's/^{"id":\([0-9][0-9]*\),.*"login":"\([^"]*\)".*/\1 \2/p' | while read -r id login; do
      case "$login" in workload:*"$SUBJECT_SUFFIX") ;; *) continue ;; esac
      c=${login#workload:}; c=${c%"$SUBJECT_SUFFIX"}
      keep=false
      for k in $CLUSTERS; do [ "$k" = "$c" ] && keep=true; done
      [ "$keep" = true ] && continue
      call DELETE "/api/admin/users/$id"
      if [ "$code" = 200 ]; then log "pruned operator user for $c"; else log "prune of $c failed (http status $code)"; fi
    done
  fi
fi
[ "$failed" = 0 ] || exit 1
log "done"
