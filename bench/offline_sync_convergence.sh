#!/usr/bin/env bash
# Runs the project's own measurement (scripts/bench_sync.sh); it prints one line `BENCH offline_sync_convergence <number>`. Generated: this wrapper measures nothing itself.
set -euo pipefail
cd "$(dirname "$0")/.."
bash scripts/bench_sync.sh
