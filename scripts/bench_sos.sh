#!/usr/bin/env bash
# Sends 10,000 SOS frames through Ingest to a WebSocket map client on the in-process stack and prints: BENCH sos_to_map_p99 <milliseconds>
set -euo pipefail
cd "$(dirname "$0")/../services/telemetry-go"
out=$(go test -run TestSOSToMapLatency -count=1 -v . 2>&1) || { echo "$out" >&2; exit 1; }
echo "$out" | grep -E '^BENCH sos_to_map_p99 [0-9.]+$'
