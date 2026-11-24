#!/usr/bin/env bash
# Runs the project's own measurement (scripts/bench_sos.sh); it prints one line `BENCH sos_to_map_p99 <number>`. Generated: this wrapper measures nothing itself.
set -euo pipefail
cd "$(dirname "$0")/.."
bash scripts/bench_sos.sh
