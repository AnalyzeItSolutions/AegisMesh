#!/usr/bin/env bash
# Acceptance: runs what the project documents (the Makefile targets and the checker scripts) the way a user would, from a clean environment, and
# reports EVERY failure, not just the first. Exit 0 only if nothing failed. Things that cannot be verified here are listed, never counted as passed.
#
#   bash scripts/acceptance.sh
#
# Optional (slow or heavy, off by default): ACCEPT_CHAOS=1 also runs the zero-drop chaos test (about 3 minutes, needs Docker).
set -uo pipefail
cd "$(dirname "$0")/.."
WORK=$(mktemp -d)
FAILURES=()
SKIPPED=()
STARTED_STACK=""

# The Python environment the services' tests and tools need: the project's own (`make setup` builds .venv), else the one the builder made. Without
# one, every Python check below would fail for the same reason, so say it once.
PYENV_USED=""
for v in .venv .builder/venv; do
  if [ -x "$v/bin/python" ]; then export PATH="$PWD/$v/bin:$PATH"; PYENV_USED=$v; break; fi
done
if [ -z "$PYENV_USED" ]; then
  FAILURES+=("no Python environment with the services' dependencies was found: run 'make setup' first")
  echo "FAIL  Python environment (run 'make setup')"
else
  echo "Python environment: $PYENV_USED"
fi

# a command as a user would run it: no PYTHONPATH or virtualenv variables the user would not have set
clean() { env -u PYTHONPATH -u VIRTUAL_ENV "$@"; }

# soft NAME COMMAND...: run, record a failure with the end of its output, and carry on with every check that does not depend on this one
soft() {
  local name=$1; shift
  local log="$WORK/$(echo "$name" | tr -c 'A-Za-z0-9' '_').log"
  if "$@" >"$log" 2>&1; then
    echo "PASS  $name"
  else
    echo "FAIL  $name"
    FAILURES+=("$name"$'\n'"$(tail -n 12 "$log" | sed 's/^/        /')")
  fi
}

skip() { echo "SKIP  $1: $2"; SKIPPED+=("$1: $2"); }

cleanup() {
  if [ -n "$STARTED_STACK" ]; then docker compose down -v >/dev/null 2>&1; fi
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "== make test (every service's own tests), in the background"
( clean make test >"$WORK/make_test.log" 2>&1; echo $? >"$WORK/make_test.rc" ) &
MAKE_TEST_PID=$!

echo "== generated files and service wiring"
soft "migrations are valid SQL with row-level security" clean python3 scripts/check_generated.py sql db/migrations
soft "Kubernetes manifests are valid" clean python3 scripts/check_generated.py k8s infra/k8s
soft "Makefile and CI files exist" clean python3 scripts/check_generated.py files Makefile .github/workflows/ci.yml
soft "every service in the contracts is registered by hand-written server code" clean python3 scripts/check_services.py --proto proto --src .
soft "generated Python stubs load" clean python3 -c 'import sys; sys.path.insert(0, "gen/py"); import aegismesh.v1.dispatch_pb2_grpc, aegismesh.v1.routing_pb2_grpc, aegismesh.v1.sync_pb2_grpc, aegismesh.v1.telemetry_pb2_grpc, aegismesh.v1.triage_pb2_grpc'
soft "generated Go stubs compile" bash -c 'cd gen/go && go vet ./...'

echo "== the measured targets (read from the system sheet, so the numbers are not repeated here)"
if [ -f .builder/sheet.json ]; then
  while read -r name op value unit; do
    [ -n "$name" ] || continue
    soft "$name $op $value$unit" clean python3 scripts/bench_assert.py "$name" "$op" "$value" --cmd "bash bench/$name.sh" --unit "$unit"
  done < <(python3 - <<'PY'
import json
for t in json.load(open(".builder/sheet.json")).get("targets", []):
    print(t["name"], t["op"], f"{t['value']:g}", t.get("unit", ""))
PY
)
else
  FAILURES+=("the system sheet (.builder/sheet.json) is missing, so the targets cannot be read")
  echo "FAIL  no system sheet"
fi

echo "== web app"
if [ -d apps/web/node_modules ]; then
  soft "web app: unit tests" bash -c 'cd apps/web && node --test'
else
  skip "web app: unit tests" "apps/web/node_modules is not installed (run npm install in apps/web)"
fi

echo "== infrastructure code"
if command -v tofu >/dev/null 2>&1 || command -v terraform >/dev/null 2>&1; then
  TF=$(command -v terraform || command -v tofu)
  soft "terraform is formatted" bash -c "cd infra/terraform && $TF fmt -check -recursive"
else
  skip "terraform format" "neither terraform nor tofu is installed"
fi

echo "== the running stack"
if docker info >/dev/null 2>&1; then
  if [ -z "$(docker compose ps -q 2>/dev/null)" ]; then STARTED_STACK=1; fi
  soft "docker compose up --wait" docker compose up -d --wait
  soft "stack: services, migrations, row-level security, Redis, Redpanda, Keycloak, Envoy, Vault" clean python3 scripts/stack_check.py
  if [ -n "${ACCEPT_CHAOS:-}" ]; then
    [ -n "$STARTED_STACK" ] && { docker compose down -v >/dev/null 2>&1; STARTED_STACK=""; }
    soft "zero-drop chaos test" bash scripts/chaos/zero_drop.sh
  else
    skip "zero-drop chaos test" "off by default, about 3 minutes (run with ACCEPT_CHAOS=1)"
  fi
else
  skip "running stack (compose, migrations on real Postgres, row-level security, chaos test)" "no Docker daemon is running"
fi

echo "== waiting for make test"
wait "$MAKE_TEST_PID"
if [ "$(cat "$WORK/make_test.rc" 2>/dev/null)" = "0" ]; then
  echo "PASS  make test"
else
  echo "FAIL  make test"
  FAILURES+=("make test"$'\n'"$(tail -n 20 "$WORK/make_test.log" | sed 's/^/        /')")
fi

# what the build itself recorded as not buildable on this machine
if [ -f .builder/ledger.json ]; then
  while IFS= read -r line; do [ -n "$line" ] && skip "$line" "recorded as blocked by the builder"; done < <(python3 - <<'PY'
import json
d = json.load(open(".builder/ledger.json"))
titles = {s["id"]: s["title"] for s in d["stages"]}
for k, why in d.get("blocked", {}).items():
    if d["status"].get(k) == "blocked":
        print(f"{titles.get(k, k)} ({why[:90]})")
PY
)
fi

echo
if [ ${#SKIPPED[@]} -gt 0 ]; then
  echo "NOT VERIFIED HERE (${#SKIPPED[@]}):"
  printf '  - %s\n' "${SKIPPED[@]}"
fi
if [ ${#FAILURES[@]} -gt 0 ]; then
  echo
  echo "FAILURES (${#FAILURES[@]}):"
  for f in "${FAILURES[@]}"; do echo "  * $f"; done
  echo "ACCEPTANCE: FAIL"
  exit 1
fi
echo "ACCEPTANCE: PASS (everything that can be verified here)"
