#!/usr/bin/env bash
# Creates the topics from the system sheet (idempotent). Needs the redpanda container from docker-compose.yml.
set -euo pipefail
docker compose exec -T redpanda rpk topic create telemetry.unit.locations -p 12 >/dev/null 2>&1 || true
docker compose exec -T redpanda rpk topic create incidents.raw.ingested -p 6 >/dev/null 2>&1 || true
docker compose exec -T redpanda rpk topic create incidents.verified.events -p 6 >/dev/null 2>&1 || true
docker compose exec -T redpanda rpk topic create logistics.routes.updated -p 6 >/dev/null 2>&1 || true
docker compose exec -T redpanda rpk topic create audit.security.immutable -p 3 >/dev/null 2>&1 || true
docker compose exec -T redpanda rpk topic list
