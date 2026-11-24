#!/usr/bin/env bash
# Zero-drop chaos test: while a writer reports incidents at a steady rate, kill Redpanda's container, bring it back, then SIGKILL the dispatch
# service and restart it. Afterwards every incident the service ACKNOWLEDGED must be in the Postgres event store and on the Redpanda topic,
# and the tenant's hash chain must verify.
#
# Needs a running Docker daemon (Postgres and Redpanda come from docker-compose.yml), Go and Python with grpcio. The dispatch service runs on the
# host, against those containers, so it can be killed like a real process.
#
# Toxiproxy is not used: docker-compose.yml has no Toxiproxy container, and killing a container and a process is the failure the brief names.
# (infra/toxiproxy/toxiproxy.json is there for latency and packet-loss experiments, which this test does not need.)
#
# Exit codes: 0 every accepted write survived; 1 a write was lost or the chain is broken; 3 the test could not run (it never reports a pass then).
# Environment: CHAOS_COUNT (default 3000 incidents), CHAOS_RATE (default 200 per second), CHAOS_KEEP=1 leaves the containers running.
set -uo pipefail
cd "$(dirname "$0")/../.."

TENANT=11111111-1111-4111-8111-111111111111
ADDR=127.0.0.1:50052
PORT=${ADDR##*:}
DB_URL='postgres://postgres:dev@127.0.0.1:5432/app?sslmode=disable'
COUNT=${CHAOS_COUNT:-3000}
RATE=${CHAOS_RATE:-200}
# the project's own virtual environment when the builder made one (its protobuf matches the generated stubs), else whatever python3 is on the PATH
if [ -n "${PYTHON:-}" ]; then PY=$PYTHON; elif [ -x .builder/venv/bin/python ]; then PY=$PWD/.builder/venv/bin/python; else PY=python3; fi
WORK=$(mktemp -d)
DISPATCH_PID=""
WRITER_PID=""

needs() { echo "NEEDS: $1 (the zero-drop test cannot run without it; this is not a pass)" >&2; exit 3; }
fail() { echo "FAILED: $1" >&2; [ -f "$WORK/dispatch.log" ] && { echo "--- last lines of the dispatch log ---" >&2; tail -n 15 "$WORK/dispatch.log" >&2; }; exit 1; }

cleanup() {
  [ -n "$WRITER_PID" ] && kill "$WRITER_PID" 2>/dev/null
  [ -n "$DISPATCH_PID" ] && kill "$DISPATCH_PID" 2>/dev/null
  if [ -z "${CHAOS_KEEP:-}" ]; then docker compose down -v >/dev/null 2>&1; fi
  rm -rf "$WORK"
}
trap cleanup EXIT

command -v docker >/dev/null 2>&1 || needs "the docker command"
docker info >/dev/null 2>&1 || needs "a running Docker daemon (start Docker Desktop)"
command -v go >/dev/null 2>&1 || needs "Go"
[ -d gen/py/aegismesh ] || needs "the generated Python stubs (bash scripts/gen_stubs.sh)"
"$PY" -c 'import grpc' >/dev/null 2>&1 || needs "Python with grpcio ($PY -m pip install grpcio)"
stub_error=$("$PY" -c 'import sys; sys.path.insert(0, "gen/py"); import aegismesh.v1.dispatch_pb2_grpc' 2>&1 | tail -n 1)
[ -z "$stub_error" ] || needs "a Python whose protobuf can load the generated stubs ($PY: $stub_error). Set PYTHON to the project's venv python, or regenerate the stubs with bash scripts/gen_stubs.sh"

# The compose file publishes fixed host ports. A container of another project (or a host process) already holding one would make `compose up` fail
# with a confusing message, so say exactly what is in the way. Nothing is stopped here: that is the owner's call.
for port in 5432 9092; do
  while read -r id name; do
    [ -n "$id" ] || continue
    docker compose ps -q 2>/dev/null | grep -q "^$id" || needs "host port $port, which container '$name' holds (stop it with: docker stop $name)"
  done < <(docker ps --filter "publish=$port" --format '{{.ID}} {{.Names}}')
  holder=$(lsof -nP -iTCP:"$port" -sTCP:LISTEN 2>/dev/null | awk 'NR>1 && $1 !~ /^com\.docke|^vpnkit|^docker/ {print $1" (pid "$2")"; exit}')
  [ -z "$holder" ] || needs "host port $port, which the process $holder holds"
done

# with_timeout SECONDS COMMAND...: macOS has no timeout(1)
with_timeout() {
  local secs=$1; shift
  "$@" & local pid=$!
  ( sleep "$secs"; kill "$pid" 2>/dev/null ) & local watchdog=$!
  wait "$pid"; local rc=$?
  kill "$watchdog" 2>/dev/null; wait "$watchdog" 2>/dev/null
  return $rc
}

wait_for_port() {
  for _ in $(seq 1 150); do
    (exec 3<>"/dev/tcp/127.0.0.1/$PORT") 2>/dev/null && return 0
    sleep 0.2
  done
  fail "the dispatch service did not start listening on $ADDR"
}

start_dispatch() {
  DATABASE_URL="$DB_URL" KAFKA_BROKERS=localhost:9092 GRPC_ADDR="$ADDR" "$WORK/dispatch" >>"$WORK/dispatch.log" 2>&1 &
  DISPATCH_PID=$!
  wait_for_port
}

echo "== starting Postgres and Redpanda"
docker compose up -d --wait postgres redpanda >"$WORK/compose.log" 2>&1 || { cat "$WORK/compose.log" >&2; fail "docker compose could not start Postgres and Redpanda"; }
bash scripts/create_topics.sh >/dev/null 2>&1 || fail "could not create the topics"

echo "== building and starting the dispatch service"
(cd services/dispatch-go && go build -o "$WORK/dispatch" .) || fail "dispatch-go does not build"
start_dispatch

echo "== sending $COUNT incidents at $RATE/s while Redpanda and then dispatch are killed"
"$PY" scripts/chaos/writer.py send --addr "$ADDR" --tenant "$TENANT" --count "$COUNT" --rate "$RATE" --out "$WORK/accepted.txt" --run-id "chaos-$$" >"$WORK/writer.json" &
WRITER_PID=$!

sleep 3
echo "CHAOS: killing redpanda";            docker compose kill redpanda >/dev/null 2>&1
sleep 3
echo "CHAOS: starting redpanda";           docker compose start redpanda >/dev/null 2>&1
sleep 3
echo "CHAOS: SIGKILL dispatch";            kill -9 "$DISPATCH_PID" 2>/dev/null; wait "$DISPATCH_PID" 2>/dev/null
sleep 2
echo "CHAOS: restarting dispatch";         start_dispatch

wait "$WRITER_PID"; WRITER_RC=$?
WRITER_PID=""
echo "writer: $(cat "$WORK/writer.json")"
[ "$WRITER_RC" -eq 0 ] || fail "the writer got no write accepted at all"
docker compose up -d --wait redpanda >/dev/null 2>&1 || fail "redpanda did not come back healthy"

collect() {
  docker compose exec -T postgres psql -U postgres -d app -At -c \
    "select aggregate_id from event_store where tenant_id = '$TENANT' and event_type = 'IncidentReported'" >"$WORK/db.txt" 2>"$WORK/psql.err" || return 1
  # How many messages the topic holds: the sum of the partitions' high watermarks (`-o start:end` is refused by rpk when any range is empty).
  local total
  total=$(docker compose exec -T redpanda rpk topic describe incidents.verified.events -p 2>"$WORK/rpk.err" | awk 'NR > 1 { s += $NF } END { print s + 0 }')
  echo "topic incidents.verified.events holds $total message(s)"
  if [ "$total" -gt 0 ]; then
    with_timeout 90 docker compose exec -T redpanda rpk topic consume incidents.verified.events -o start -n "$total" -f '%v\n' >"$WORK/topic.txt" 2>>"$WORK/rpk.err" || return 1
  else
    : >"$WORK/topic.txt"       # nothing was published: the judge reports every accepted write as missing from the topic
  fi
  "$PY" scripts/chaos/writer.py chain --addr "$ADDR" --tenant "$TENANT" >"$WORK/chain.json" 2>"$WORK/chain.err" || true
}

# The outbox publishes in the background, so the topic may take a little to catch up after the last write: look again for a while.
echo "== checking what survived"
VERDICT=1
for attempt in 1 2 3 4 5 6 7 8 9 10 11 12; do
  collect || { cat "$WORK/psql.err" "$WORK/rpk.err" >&2 2>/dev/null; fail "could not read the database or the topic back"; }
  if "$PY" scripts/chaos/writer.py verify --accepted "$WORK/accepted.txt" --db "$WORK/db.txt" --topic "$WORK/topic.txt" --chain "$WORK/chain.json" >"$WORK/verdict.txt" 2>&1; then VERDICT=0; break; fi
  sleep 5
done
cat "$WORK/verdict.txt"
DUPLICATES=$(sort "$WORK/db.txt" | uniq -d | head -n 3)
[ -z "$DUPLICATES" ] || { echo "the event store holds the same incident twice, for example: $DUPLICATES" >&2; VERDICT=1; }
exit "$VERDICT"
