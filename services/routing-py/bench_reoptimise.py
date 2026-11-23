"""50 vehicles x 200 incidents: plan, add a hazard polygon over gRPC, and time the re-optimisation it triggers.
Prints one line: BENCH reoptimise_50x200 <seconds>   (exit 1 if the plan is wrong)."""
import json
import pathlib
import random
import sys
import time

HERE = pathlib.Path(__file__).resolve().parent
for p in (HERE, HERE.parent.parent / "gen" / "py", HERE / "routing_vrp", HERE / "geo_h3"):
    sys.path.insert(0, str(p))

import grpc  # noqa: E402
import server  # noqa: E402
from aegismesh.v1 import common_pb2, routing_pb2, routing_pb2_grpc  # noqa: E402

TENANT = "11111111-1111-4111-8111-111111111111"
AUTH = (("x-tenant-id", TENANT),)
VEHICLES, INCIDENTS, CAPACITY = 50, 200, 8
DEPOT = {"lat": 12.95, "lon": 77.55}


def fail(msg: str) -> None:
    print(f"FAILED: {msg}", file=sys.stderr)
    sys.exit(1)


def main() -> None:
    rng = random.Random(7)
    request = {
        "vehicles": [{"id": f"v{i}", "capacity": CAPACITY, "start": DEPOT} for i in range(VEHICLES)],
        "incidents": [{"id": f"i{k}", "lat": DEPOT["lat"] + rng.uniform(-0.08, 0.08), "lon": DEPOT["lon"] + rng.uniform(-0.08, 0.08),
                       "demand": rng.randint(1, 2), "urgency": rng.randint(1, 5)} for k in range(INCIDENTS)],
    }
    srv, port = server.make_server("127.0.0.1:0")
    srv.start()
    chan = grpc.insecure_channel(f"127.0.0.1:{port}")
    stub = routing_pb2_grpc.RoutingServiceStub(chan)
    try:
        before = json.loads(stub.Optimise(routing_pb2.RoutingRequest(payload=json.dumps(request).encode()), metadata=AUTH).payload)
        hazard = {"id": "flood-1", "kind": "flood", "polygon": [[77.57, 12.92], [77.63, 12.92], [77.63, 12.98], [77.57, 12.98]]}   # [lon, lat], a flooded block east of the depot
        start = time.perf_counter()
        ack = json.loads(stub.BlockEdges(common_pb2.HazardPolygon(payload=json.dumps(hazard).encode()), metadata=AUTH).payload)
        elapsed = time.perf_counter() - start
    finally:
        chan.close()
        srv.stop(0)

    after = ack.get("plan")
    if not ack.get("replanned") or not after:
        fail("the hazard did not trigger a re-plan")
    where = {i["id"]: (i["lon"], i["lat"]) for i in request["incidents"]}
    demand = {i["id"]: i["demand"] for i in request["incidents"]}
    poly = [tuple(p) for p in hazard["polygon"]]
    depot = (DEPOT["lon"], DEPOT["lat"])
    seen = []
    for r in after["routes"]:
        if sum(demand[s] for s in r["stops"]) > CAPACITY:
            fail(f"vehicle {r['vehicle_id']} is over capacity")
        if not r["stops"]:
            continue
        legs = [depot] + [where[s] for s in r["stops"]] + [depot]
        for a, b in zip(legs, legs[1:]):
            if server.vrp.blocked_edges([a, b], [poly]):
                fail(f"vehicle {r['vehicle_id']} has a leg through the hazard")
        seen += r["stops"]
    if len(seen) != len(set(seen)) or set(seen) | set(after["unassigned"]) != set(where) or set(seen) & set(after["unassigned"]):
        fail("every incident must be served once or listed as unassigned, never both or twice")
    if len(seen) < 0.8 * INCIDENTS:
        fail(f"only {len(seen)} of {INCIDENTS} incidents were served")
    print(f"{VEHICLES} vehicles, {INCIDENTS} incidents: {len(before['unassigned'])} unassigned before the hazard, {len(after['unassigned'])} after", file=sys.stderr)
    print(f"BENCH reoptimise_50x200 {elapsed:.3f}")


if __name__ == "__main__":
    main()
