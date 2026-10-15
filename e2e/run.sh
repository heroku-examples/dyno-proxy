#!/usr/bin/env bash
# End-to-end test of the dyno-proxy buildpack on real Heroku apps.
#
# Each app gets the Rack app in e2e/app deployed in 4 variants
#   (dyno-proxy buildpack first|last) x (Procfile present|absent)
# and every variant is checked in the default state (proxy active). Apps run in
# parallel; variants within an app run serially.
#
# After the default state passes, each variant also runs the disable and crash
# states (they run on a variant only after its default state passed). Run a
# subset with, e.g.:
#   STATES="default disable" e2e/run.sh my-test-app
#
# States:
#   default        expect the proxy to be active
#   disable        DISABLE_DYNO_PROXY=true, expect the proxy to be inactive
#   dyno-proxy-crash  GET /crash/dyno-proxy, expect the dyno to restart (new boot id)
#   app-crash         GET /crash/app, expect the dyno to restart (new boot id)
#
# Proxy detection: project.toml gets a probe route
#   /anything/dyno-proxy-e2e* -> https://httpbin.org
# Through the proxy httpbin answers with its JSON echo; without it the app
# answers 404 "dyno-proxy-e2e-app: not found". GET / must return the app page.
#
# Crash cells start from `heroku ps:scale web=0` + `web=1`: the Common Runtime
# imposes a cool-off on repeated crashes, and only scaling resets it.
#
# Classic vs CNB is detected from the app's stack ("cnb" => CNB).
#
# Usage:
#   e2e/run.sh my-app-cnb my-app-classic   # one or more dedicated test apps
#
# Env overrides:
#   CNB_BUILDPACK_URL      dyno-proxy buildpack for CNB apps (project.toml uri)
#   CLASSIC_BUILDPACK_URL  dyno-proxy buildpack for classic apps
#   APP_DIR, WORK_DIR      app to deploy; scratch dir for repos and logs
#   ORDERS="first last"  PROCFILES="present absent"  STATES="default disable dyno-proxy-crash app-crash"
#   TIMEOUT                seconds to wait for a release/restart (default 90)
#
# WARNING: force-pushes to, restarts, and changes buildpacks/config vars on, the
# target apps. Exits non-zero if any cell fails.
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
CLASSIC_BUILDPACK_URL="${CLASSIC_BUILDPACK_URL:-https://github.com/heroku-examples/dyno-proxy/releases/latest/download/dyno-proxy-buildpack.tgz}"
CNB_BUILDPACK_URL="${CNB_BUILDPACK_URL:-docker://ghcr.io/heroku-examples/dyno-proxy-buildpack}"
APP_DIR="${APP_DIR:-$ROOT_DIR/e2e/app}"
WORK_DIR="${WORK_DIR:-$ROOT_DIR/tmp/e2e}"
ORDERS="${ORDERS:-first last}"
PROCFILES="${PROCFILES:-present absent}"
STATES="${STATES:-default disable dyno-proxy-crash app-crash}"
TIMEOUT="${TIMEOUT:-90}"

PROBE_PATH="/anything/dyno-proxy-e2e"
APP_MARKER="dyno-proxy e2e app"
TS="$(date -u +%Y%m%dT%H%M%SZ)"
EVID="$WORK_DIR/$TS"

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
die() { log "ERROR: $*" >&2; exit 1; }
now() { date +%s; }

# Prints proxy-active, proxy-inactive or other, and requires GET / to be the app.
probe() {
  local url=$1 dir=$2 rc pc
  rc=$(curl -s -o "$dir/root.html" -w '%{http_code}' --max-time 15 "$url/") || rc=000
  pc=$(curl -s -o "$dir/probe.body" -w '%{http_code}' --max-time 15 "$url$PROBE_PATH") || pc=000
  if [ "$rc" != 200 ] || ! grep -qF "$APP_MARKER" "$dir/root.html"; then echo "other(root $rc)"
  elif [ "$pc" = 200 ] && grep -q '"Host": *"httpbin.org"' "$dir/probe.body"; then echo proxy-active
  elif [ "$pc" = 404 ] && grep -qF "dyno-proxy-e2e-app: not found" "$dir/probe.body"; then echo proxy-inactive
  else echo "other(probe $pc)"; fi
}

# Poll until the probe shows $3 or TIMEOUT passes; prints the last observation.
wait_probe() {
  local url=$1 dir=$2 want=$3 deadline=$(( $(now) + TIMEOUT )) got
  while :; do
    got=$(probe "$url" "$dir")
    { [ "$got" = "$want" ] || [ "$(now)" -ge "$deadline" ]; } && break
    sleep 3
  done
  echo "$got"
}

# Wait until every web dyno runs release >= $2 and is up.
wait_ready() {
  local app=$1 version=$2 deadline=$(( $(now) + TIMEOUT ))
  until heroku ps -a "$app" --json 2>/dev/null | jq -e --argjson v "$version" '
      [.[] | select(.type == "web")] | length > 0 and all(.release.version >= $v and (.state == "up" or .state == "idle"))' >/dev/null; do
    [ "$(now)" -ge "$deadline" ] && return 1
    sleep 3
  done
}

latest_release() { heroku releases -a "$1" --json -n 1 2>/dev/null | jq -r 'max_by(.version).version // 0'; }

boot_id() { curl -s --max-time 10 "$1/boot" | jq -r '.boot_id // empty' 2>/dev/null; }

set_disable() { # app true|""
  if [ -n "$2" ]; then heroku config:set DISABLE_DYNO_PROXY=true -a "$1"; else heroku config:unset DISABLE_DYNO_PROXY -a "$1"; fi >/dev/null 2>&1
}

write_project_toml() {
  local kind=$1 order=$2 bp ruby
  bp="[[io.buildpacks.group]]
uri = \"$CNB_BUILDPACK_URL\"
"
  ruby='[[io.buildpacks.group]]
id = "heroku/ruby"
'
  {
    printf '[_]\nschema-version = "0.2"\n\n'
    if [ "$kind" = cnb ]; then
      if [ "$order" = first ]; then printf '%s\n%s\n' "$bp" "$ruby"; else printf '%s\n%s\n' "$ruby" "$bp"; fi
    fi
    printf '[[routes]]\npath = "%s*"\nto = "https://httpbin.org"\n' "$PROBE_PATH"
  } > project.toml
}

set_classic_buildpacks() {
  local app=$1 order=$2 first=$CLASSIC_BUILDPACK_URL second=heroku/ruby
  [ "$order" = last ] && { first=heroku/ruby; second=$CLASSIC_BUILDPACK_URL; }
  heroku buildpacks:clear -a "$app" >/dev/null 2>&1
  heroku buildpacks:add "$first" -a "$app" >/dev/null && heroku buildpacks:add "$second" -a "$app" >/dev/null
}

push_app() { # repo log; retries transient git/network errors
  local attempt
  for attempt in 1 2 3; do
    ( cd "$1" && git push --force heroku HEAD:main ) > "$2" 2>&1 && return 0
    grep -qE 'cannot lock ref|RPC failed|Connection reset|Could not resolve host|early EOF|returned error: 5' "$2" || return 1
    sleep 10
  done
  return 1
}

# Delete the app's build cache so each deploy starts from scratch (a stale
# cached dyno-proxy/traefik from an earlier variant could mask a build problem).
# curl -v output goes to the evidence dir. Failure is logged, not fatal.
clear_build_cache() {
  local app=$1 logfile=$2 url
  url=$(heroku api -v 3.sdk "/apps/$app/build-metadata" 2>>"$logfile" | jq -r '.cache_delete_url // empty')
  if [ -z "$url" ]; then
    log "WARN cannot read cache_delete_url for $app; build cache not cleared"
    return
  fi
  curl -v -X DELETE "$url" >>"$logfile" 2>&1 || log "WARN build cache delete failed for $app (see $logfile)"
}

# Run one state against the deployed variant; prints "PASS|FAIL <observed>".
run_state() {
  local app=$1 url=$2 dir=$3 state=$4 got before version
  mkdir -p "$dir"
  case "$state" in
    default|disable)
      if [ "$state" = disable ]; then set_disable "$app" true; else set_disable "$app" ""; fi
      wait_ready "$app" "$(latest_release "$app")"
      got=$(wait_probe "$url" "$dir" "$([ "$state" = disable ] && echo proxy-inactive || echo proxy-active)")
      [ "$got" = "$([ "$state" = disable ] && echo proxy-inactive || echo proxy-active)" ] && echo "PASS $got" || echo "FAIL $got"
      ;;
    dyno-proxy-crash|app-crash)
      set_disable "$app" ""
      version=$(latest_release "$app")
      heroku ps:scale web=0 -a "$app" >/dev/null 2>&1
      heroku ps:scale web=1 -a "$app" >/dev/null 2>&1
      wait_ready "$app" "$version"
      got=$(wait_probe "$url" "$dir" proxy-active)
      before=$(boot_id "$url")
      [ "$got" = proxy-active ] && [ -n "$before" ] || { echo "FAIL precondition($got)"; return; }
      curl -s -o "$dir/crash.json" --max-time 15 "$url/crash/${state%-crash}"
      local deadline=$(( $(now) + TIMEOUT )) id
      while [ "$(now)" -lt "$deadline" ]; do
        sleep 4
        id=$(boot_id "$url")
        if [ -n "$id" ] && [ "$id" != "$before" ]; then
          got=$(wait_probe "$url" "$dir" proxy-active)
          [ "$got" = proxy-active ] && echo "PASS restarted" || echo "FAIL restarted-without-proxy($got)"
          return
        fi
      done
      echo "FAIL no-restart"
      ;;
    *) die "unknown state $state" ;;
  esac
}

run_variant() {
  local app=$1 url=$2 kind=$3 order=$4 pf=$5 repo=$6
  local name="$order/$pf" vdir="$EVID/$app/$order-$pf" state out
  mkdir -p "$vdir"
  (
    cd "$repo" || exit 1
    git checkout --quiet -B "e2e-$order-$pf" main
    write_project_toml "$kind" "$order"
    [ "$pf" = absent ] && git rm --quiet -f Procfile
    git add -A
    git commit --quiet --allow-empty -m "dyno-proxy e2e $TS: $order $pf"
  ) || { echo "FAIL $name setup"; return; }
  [ "$kind" = classic ] && { set_classic_buildpacks "$app" "$order" || { echo "FAIL $name buildpacks"; return; }; }
  set_disable "$app" ""
  clear_build_cache "$app" "$vdir/clear-cache.log"
  if ! push_app "$repo" "$vdir/build.log"; then
    echo "FAIL $name build ($vdir/build.log)"; return
  fi
  for state in $STATES; do
    out=$(run_state "$app" "$url" "$vdir/$state" "$state")
    echo "${out%% *} $name $state: ${out#* }"
    # Extra states only make sense on a variant whose default state works.
    [ "$state" = default ] && [ "${out%% *}" != PASS ] && return
  done
}

run_app() {
  local app=$1 info kind url repo="$EVID/$1/repo" order pf
  info=$(heroku apps:info -a "$app" --json 2>/dev/null) || { echo "FAIL cannot read app info"; return; }
  kind=$(jq -r 'if .app.stack.name == "cnb" then "cnb" else "classic" end' <<<"$info")
  url=$(jq -r '.app.web_url | sub("/$"; "")' <<<"$info")
  mkdir -p "$repo"
  rsync -a --exclude .git --exclude .bundle --exclude vendor/bundle --exclude tmp --exclude log "$APP_DIR/" "$repo/"
  ( cd "$repo" && git init --quiet -b main && git add -A && git commit --quiet -m "dyno-proxy e2e app" \
      && git remote add heroku "https://git.heroku.com/$app.git" ) || { echo "FAIL cannot prepare repo"; return; }
  heroku ps:scale web=1 -a "$app" >/dev/null 2>&1
  for order in $ORDERS; do
    for pf in $PROCFILES; do
      run_variant "$app" "$url" "$kind" "$order" "$pf" "$repo"
    done
  done
  set_disable "$app" ""
}

main() {
  command -v heroku >/dev/null || die "heroku CLI not found"
  command -v jq >/dev/null || die "jq not found"
  heroku auth:whoami >/dev/null 2>&1 || die "not logged in to Heroku (run: heroku login)"
  [ -f "$APP_DIR/config.ru" ] || die "APP_DIR $APP_DIR has no config.ru"

  [ $# -gt 0 ] || die "usage: e2e/run.sh <app> [<app>...]"
  local apps="$*" app
  mkdir -p "$EVID"
  log "apps=[$apps] orders=[$ORDERS] procfiles=[$PROCFILES] states=[$STATES]; evidence: $EVID"

  # Stop everything on Ctrl-C: restore default handlers first (so `kill 0`, which
  # also signals this shell, terminates it instead of re-entering the trap), then
  # signal the whole process group: the per-app subshells and any
  # heroku/git/curl/sleep in flight.
  trap 'trap - INT TERM; log interrupted >&2; kill -TERM 0' INT TERM
  for app in $apps; do
    ( run_app "$app" 2>&1 | sed -u "s/^/[$app] /" | tee "$EVID/$app.log" ) &
  done
  wait

  local pass fail
  pass=$(cat "$EVID"/*.log 2>/dev/null | grep -c '\] PASS ')
  fail=$(cat "$EVID"/*.log 2>/dev/null | grep -c '\] FAIL ')
  log "$pass passed, $fail failed"
  [ "$fail" -eq 0 ]
}

main "$@"
