#!/usr/bin/env bash
# ONE live check of macula-cli against a macula 12 station, with keys made for
# the run and never saved (-ephemeral). Defaults: helsinki and the io.macula
# realm; the realm key is io.macula's PUBLIC half, read from mcl-echo's
# header. Override any MACULA_CLI_LIVE_* variable to point elsewhere.
#
# Checks: connect (resolve, pinned link), node records in the DHT, mcl-echo/echo
# by direct dial, and a watch hearing one publication on a random topic. It
# puts nothing in the DHT and publishes once. Exits 1 on the first failure.
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
echo_hrl="${MCL_ECHO_REALM_HRL:-$HOME/work/github.com/macula-services/mcl-echo/apps/mcl_echo/include/mcl_echo_io_macula.hrl}"

realm_key_from_hrl() {
  python3 - "$1" <<'PY'
import re, sys
text = open(sys.argv[1]).read()
body = text[text.index("MCL_ECHO_REAL_REALM_KEY"):]
body = body[:body.index(">>")]
print("".join(re.findall(r"16#([0-9A-Fa-f]+):\d+", body)).lower())
PY
}

seed="${MACULA_CLI_LIVE_SEED:-station-fi-helsinki.macula.io:4433@004d1f470097ccf8826ce291900e882fdb1f20375e53901facaec0f23eb4efd8}"
realm="${MACULA_CLI_LIVE_REALM:-io.macula}"
realm_key="${MACULA_CLI_LIVE_REALM_KEY:-$(realm_key_from_hrl "$echo_hrl")}"
[ -n "$realm_key" ] || { echo "live_check: the realm key is empty" >&2; exit 1; }
echo "realm key: $(( ${#realm_key} / 2 )) bytes"

cd "$here"
bin="$(mktemp -d)/macula-cli"
trap 'rm -rf "$(dirname "$bin")"' EXIT
go build -o "$bin" ./cmd/macula-cli
mesh=(-seed "$seed" -ephemeral -timeout 60s)
realmed=("${mesh[@]}" -realm "$realm" -realm-key "$realm_key")

date -u +"live start %FT%TZ"
echo "== connect";                "$bin" connect "${mesh[@]}"
echo "== node records";           "$bin" dht find-records-by-type "${mesh[@]}" node_record | tail -1
echo "== mcl-echo/echo";          out=$("$bin" call "${realmed[@]}" -payload '"hello"' mcl-echo/echo)
echo "$out"; [ "$out" = '"hello"' ] || { echo "live_check: mcl-echo answered $out" >&2; exit 1; }
topic="mcl-cli/live/check/publication_heard_v1/$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
echo "== hear own publication on $topic"
"$bin" pubsub watch "${realmed[@]}" -count 1 -for 45s -json "$topic" > "$(dirname "$bin")/heard.json" &
watcher=$!
sleep 8
"$bin" pubsub publish "${realmed[@]}" -payload '"heard"' "$topic"
wait "$watcher"
grep -q '"heard"' "$(dirname "$bin")/heard.json" || { echo "live_check: the publication was not heard" >&2; cat "$(dirname "$bin")/heard.json"; exit 1; }
echo "heard it"
date -u +"live end %FT%TZ"
