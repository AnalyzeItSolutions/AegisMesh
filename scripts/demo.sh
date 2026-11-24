#!/usr/bin/env bash
# One command to see AegisMesh working: the real backing stack in Docker (Postgres + PostGIS + pgvector, Redis, Redpanda, Keycloak, Envoy gateway),
# the three Go services on this machine, the web app, and a simulated fleet of units. Open http://localhost:3000 and sign in as alice / alice-dev.
# Stop everything with Ctrl-C. Needs Docker, Go, Node 20+ and Python 3 with the project's .venv (make setup).
set -uo pipefail
cd "$(dirname "$0")/.."
WORK=$(mktemp -d)
PIDS=()
needs() { echo "NEEDS: $1" >&2; exit 3; }
cleanup() { for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done; docker compose down -v >/dev/null 2>&1; rm -rf "$WORK"; echo "stopped"; }
trap cleanup EXIT INT TERM
for c in docker go node npm; do command -v $c >/dev/null 2>&1 || needs "the $c command"; done
docker info >/dev/null 2>&1 || needs "a running Docker daemon"
if [ -x .venv/bin/python ]; then PY=.venv/bin/python; elif [ -x .builder/venv/bin/python ]; then PY=.builder/venv/bin/python; else needs "the Python environment: run make setup first"; fi
for port in 3000 8080 8081 5432 6379 9092 50051 50052 50054 8051 8052 8084; do
  lsof -iTCP:$port -sTCP:LISTEN >/dev/null 2>&1 && needs "port $port is already in use (stop what is using it, or run make down)"
done

echo "== starting Postgres, Redis, Redpanda, Keycloak, gateway"
docker compose up -d --wait postgres redis redpanda keycloak envoy >/dev/null 2>&1 || needs "the Docker stack did not start (docker compose logs shows why)"
bash scripts/create_topics.sh >/dev/null 2>&1 || needs "the Redpanda topics could not be created"

echo "== building and starting the services"
DB='postgres://postgres:dev@127.0.0.1:5432/app?sslmode=disable'
for s in "telemetry-go:50054:8084" "sync-go:50051:8051" "dispatch-go:50052:8052"; do
  IFS=: read -r name grpc http <<<"$s"
  (cd services/$name && go build -o "$WORK/$name" .) || needs "the $name service did not build"
  GRPC_ADDR=:$grpc HTTP_ADDR=:$http DATABASE_URL=$DB KAFKA_BROKERS=127.0.0.1:9092 REDIS_ADDR=127.0.0.1:6379 "$WORK/$name" >"$WORK/$name.log" 2>&1 &
  PIDS+=($!)
done

echo "== building the web app"
(cd apps/web && { [ -d node_modules ] || npm ci --silent; } && npx next build >"$WORK/web-build.log" 2>&1) || needs "the web app did not build (see $WORK/web-build.log)"
(cd apps/web && exec npx next start -p 3000 >"$WORK/web.log" 2>&1) &
PIDS+=($!)
sleep 4

cat <<MSG

  AegisMesh is running.
    Command center  http://localhost:3000/command
    Field app       http://localhost:3000/field   (works offline; reports sync when the connection returns)
    Sign in as      alice / alice-dev   (tenant A)     bob / bob-dev   (tenant B: sees none of alice's data)

  A simulated fleet is driving around Bengaluru below. Ctrl-C stops everything.

MSG
$PY scripts/simulate.py
