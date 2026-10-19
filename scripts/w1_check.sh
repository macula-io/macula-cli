#!/usr/bin/env bash
# ONE read-only check of macula-cli against a wire 1.0 station, with a key made
# for the run and never saved (-ephemeral). Point it at a station already on
# wire 1.0:
#
#   MACULA_CLI_W1_SEED='pq.example.invalid:4433@<station node_id hex>' scripts/w1_check.sh
#
# It links to the station it pins, and reads node records and station endpoint
# records from the DHT through it. It puts nothing in the DHT, subscribes to
# nothing and publishes nothing: a station that holds no realm needs none. The
# seed has no default on purpose, so no station of ours is named in this
# repository. Exits 1 on the first failure.
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"

seed="${MACULA_CLI_W1_SEED:-}"
[ -n "$seed" ] || {
  echo "w1_check: set MACULA_CLI_W1_SEED=host[:port]@<station node_id hex>" >&2
  exit 1
}

cd "$here"
bin="$(mktemp -d)/macula-cli"
trap 'rm -rf "$(dirname "$bin")"' EXIT
go build -o "$bin" ./cmd/macula-cli
mesh=(-seed "$seed" -ephemeral -timeout 60s)

date -u +"w1 check start %FT%TZ"
echo "== connect";                  "$bin" connect "${mesh[@]}"
echo "== node records";             "$bin" dht find-records-by-type "${mesh[@]}" node_record | tail -1
echo "== station endpoint records"; "$bin" dht find-records-by-type "${mesh[@]}" station_endpoint | tail -1
echo "w1 check passed: linked and read the DHT, published nothing"
date -u +"w1 check end %FT%TZ"
