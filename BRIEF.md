# AegisMesh: offline-first, multi-tenant disaster-response coordination platform

Build the whole system below as a monorepo. Field responders work offline on a PWA and sync by CRDT; a command center shows live units and incidents on a map; dispatch is event-sourced; routing optimises vehicles around hazards; an AI triage worker prioritises incoming reports. Everything is multi-tenant (row-level security per tenant) and every state change is written to an append-only, hash-chained audit log.

```sheet
{
  "name": "aegismesh",
  "go_module": "github.com/aegismesh/aegismesh",
  "infra": ["postgres", "timescale", "redis", "redpanda", "keycloak", "envoy", "vault"],
  "event_store": true,
  "services": [
    {"name": "sync", "lang": "go", "dir": "services/sync-go", "port": 50051, "proto_service": "SyncService",
     "rpcs": [{"name": "PushDelta", "in": "DeltaBatch", "out": "SyncAck"}, {"name": "PullDelta", "in": "PullRequest", "out": "DeltaBatch"}, {"name": "StateHash", "in": "HashRequest", "out": "HashReply"}]},
    {"name": "dispatch", "lang": "go", "dir": "services/dispatch-go", "port": 50052, "proto_service": "DispatchService",
     "rpcs": [{"name": "ReportIncident", "in": "IncidentReport", "out": "IncidentAck"}, {"name": "DispatchUnit", "in": "DispatchRequest", "out": "DispatchAck"}, {"name": "AddHazard", "in": "HazardPolygon", "out": "HazardAck"}, {"name": "VerifyChain", "in": "VerifyRequest", "out": "VerifyReply"}]},
    {"name": "routing", "lang": "python", "dir": "services/routing-py", "port": 50053, "proto_service": "RoutingService",
     "rpcs": [{"name": "Optimise", "in": "RoutingRequest", "out": "RoutingPlan"}, {"name": "BlockEdges", "in": "HazardPolygon", "out": "HazardAck"}]},
    {"name": "telemetry", "lang": "go", "dir": "services/telemetry-go", "port": 50054, "proto_service": "TelemetryService",
     "rpcs": [{"name": "Ingest", "in": "LocationFrame", "out": "TelemetryAck"}, {"name": "Subscribe", "in": "ViewportRequest", "out": "LocationFrame"}]},
    {"name": "triage", "lang": "python", "dir": "services/triage-py", "port": 50055, "proto_service": "TriageService",
     "rpcs": [{"name": "Classify", "in": "TriageInput", "out": "TriageResult"}, {"name": "FindDuplicate", "in": "TriageInput", "out": "DuplicateResult"}]}
  ],
  "topics": [
    {"name": "telemetry.unit.locations", "partitions": 12},
    {"name": "incidents.raw.ingested", "partitions": 6},
    {"name": "incidents.verified.events", "partitions": 6},
    {"name": "logistics.routes.updated", "partitions": 6},
    {"name": "audit.security.immutable", "partitions": 3}
  ],
  "entities": [
    {"name": "tenants", "tenant": false, "fields": [{"name": "id", "type": "uuid", "pk": true}, {"name": "name", "type": "text"}, {"name": "created_at", "type": "timestamptz"}]},
    {"name": "incidents", "tenant": true, "fields": [{"name": "id", "type": "uuid", "pk": true}, {"name": "kind", "type": "text"}, {"name": "severity", "type": "text"}, {"name": "location", "type": "geo"}, {"name": "h3_cell", "type": "h3"}, {"name": "embedding", "type": "vector(384)"}, {"name": "status", "type": "text"}, {"name": "reported_at", "type": "timestamptz"}]},
    {"name": "hazard_zones", "tenant": true, "fields": [{"name": "id", "type": "uuid", "pk": true}, {"name": "polygon", "type": "geo"}, {"name": "kind", "type": "text"}, {"name": "active", "type": "bool"}]},
    {"name": "units", "tenant": true, "fields": [{"name": "id", "type": "uuid", "pk": true}, {"name": "callsign", "type": "text"}, {"name": "capacity", "type": "int"}, {"name": "location", "type": "geo"}, {"name": "status", "type": "text"}]}
  ],
  "snippets": [
    {"name": "crdt", "lang": "go", "dest": "services/sync-go/internal/crdt"},
    {"name": "hashchain", "lang": "go", "dest": "services/dispatch-go/internal/hashchain"},
    {"name": "outbox", "lang": "python", "dest": "services/triage-py/outbox"},
    {"name": "priority", "lang": "python", "dest": "services/triage-py/priority"},
    {"name": "geo_h3", "lang": "python", "dest": "services/routing-py/geo_h3"},
    {"name": "routing_vrp", "lang": "python", "dest": "services/routing-py/routing_vrp"},
    {"name": "ratelimit", "lang": "python", "dest": "services/triage-py/ratelimit"},
    {"name": "jwtclaims", "lang": "python", "dest": "services/triage-py/jwtclaims"}
  ],
  "targets": [
    {"name": "offline_sync_convergence", "op": "<", "value": 10, "unit": "s", "text": "50 offline clients, 1,000 conflicting edits, 20% packet loss: one byte-identical state hash within 10 s"},
    {"name": "sos_to_map_p99", "op": "<", "value": 150, "unit": "ms", "text": "SOS to map p99 < 150 ms"},
    {"name": "reoptimise_50x200", "op": "<", "value": 3, "unit": "s", "text": "50 vehicles x 200 incidents re-optimised under 3 seconds after a hazard"},
    {"name": "chain_tamper_detect", "op": "<", "value": 1, "unit": "s", "text": "a full chain walk finds a one-character tamper in under 1 second"}
  ]
}
```

## Monorepo
`apps/web` (Next.js), `services/sync-go`, `services/dispatch-go`, `services/routing-py`, `services/telemetry-go`, `services/triage-py`, `infra/terraform`, `proto/`, `db/migrations`, a Makefile and CI.

## Contracts
Protobuf for every gRPC call listed in the sheet and for the WebSocket telemetry frame (`LocationFrame`: unit id, lat, lng, h3 cell, heading, timestamp). Go and Python stubs are generated, never hand-written.

## Data and tenancy
PostgreSQL 16 with PostGIS and pgvector; TimescaleDB for telemetry history. Every tenant table has row-level security keyed on a short-lived JWT claim `tenant_id`; attribute-based access checks roles. PII fields are encrypted per tenant with keys from Vault. `event_store` is append-only: `sequence_id, aggregate_id, event_type, payload, actor_id, prev_hash, entry_hash`, each entry hashing the previous one with SHA-256, with a verifier that names the first broken entry.

## Sync (sync-go)
Delta-state CRDT: hybrid logical clock, last-writer-wins registers, add-wins OR-sets, canonical state hash. Clients keep a transactional outbox and push deltas when online; gRPC-Web and WebSocket transports.

## Dispatch (dispatch-go)
Event sourcing with CQRS projections; events `IncidentReported`, `UnitDispatched`, `RoadBlocked`, `HazardPolygonAdded`; resource locking so a unit is never dispatched twice. Every event is appended to the event store and published to Redpanda through the outbox.

## Routing (routing-py)
OR-Tools vehicle routing with capacity, time windows and urgency. A `HazardPolygonAdded` event blocks every road edge crossing the polygon and re-optimises. H3 cells index incidents and units.

## Telemetry and command center
Units stream locations to telemetry-go, which writes Redis Geo and Pub/Sub and Timescale, and publishes `telemetry.unit.locations`. The command center (MapLibre + Deck.gl) subscribes over WebSocket by viewport, as a set of H3 cells, renders 25,000+ markers at 60 FPS, and can replay past state (time travel) from the event store.

## Field PWA
Next.js, IndexedDB, Yjs-compatible local state, a service worker, offline map tiles. Fully usable offline; syncs on reconnect.

## AI triage (triage-py)
Reports come as text, audio or image. Classify with the START tree (Black, Red, Yellow, Green). Detect duplicates by same H3 cell, nearby time and pgvector cosine similarity. Low-confidence results go to a human queue. Any model call sits behind a flag and is off by default.

## Gateway and ops
Envoy in front: rate limit, JWT validation, mTLS between services. OpenTelemetry end to end, Prometheus, Grafana SLO dashboards, k6 load scripts, Toxiproxy chaos scripts, Kubernetes manifests, Terraform, GitHub Actions.

## Acceptance
- 50 offline clients make 1,000 conflicting edits under 20% packet loss and all end with one byte-identical state hash within 10 s.
- SOS to map p99 < 150 ms on the compose stack.
- 50 vehicles x 200 incidents re-optimised in under 3 seconds after a hazard is added.
- A full hash-chain walk finds a one-character tamper in under 1 second.
- Killing Redpanda or dispatch mid-load drops zero writes.

## Needs something outside this machine (say so, do not fake it)
Speech-to-text and image-severity model weights or API keys; a regional OSM extract for road-network routing; a cloud account for deployment; a browser run to measure 60 FPS; capacity for 10,000 concurrent k6 users.
