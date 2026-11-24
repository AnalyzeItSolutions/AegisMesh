#!/usr/bin/env bash
# Runs the project's own measurement (services/routing-py/bench_reoptimise.py); it prints one line `BENCH reoptimise_50x200 <number>`. Generated: this wrapper measures nothing itself.
set -euo pipefail
cd "$(dirname "$0")/.."
cd services/routing-py && python3 bench_reoptimise.py
