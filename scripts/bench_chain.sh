#!/usr/bin/env bash
# Builds a 100,000-event hash chain, changes one character, and prints: BENCH chain_tamper_detect <seconds>
set -euo pipefail
cd "$(dirname "$0")/../services/dispatch-go"
out=$(go test -run TestChainTamperDetect -count=1 -v . 2>&1) || { echo "$out" >&2; exit 1; }
echo "$out" | grep -E '^BENCH chain_tamper_detect [0-9.]+$'
