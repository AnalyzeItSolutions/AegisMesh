#!/usr/bin/env bash
# Runs the project's own measurement (scripts/bench_chain.sh); it prints one line `BENCH chain_tamper_detect <number>`. Generated: this wrapper measures nothing itself.
set -euo pipefail
cd "$(dirname "$0")/.."
bash scripts/bench_chain.sh
