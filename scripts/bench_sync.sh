#!/usr/bin/env bash
# Runs the 50-client, 1,000-edit, 20%-loss convergence simulation and prints: BENCH offline_sync_convergence <seconds>
set -euo pipefail
cd "$(dirname "$0")/../services/sync-go"
out=$(go test -run TestOfflineSyncConvergence -count=1 -v . 2>&1) || { echo "$out" >&2; exit 1; }
echo "$out" | grep -E '^BENCH offline_sync_convergence [0-9.]+$'
