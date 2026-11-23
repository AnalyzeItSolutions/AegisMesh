"""RoutingService over a real gRPC connection: planning, capacity, urgency, time windows, hazards and refusals."""
import json

import grpc
import h3
import pytest

pytest.importorskip("ortools")

import server  # noqa: E402
from aegismesh.v1 import common_pb2, routing_pb2, routing_pb2_grpc  # noqa: E402

TENANT = "11111111-1111-4111-8111-111111111111"
AUTH = (("x-tenant-id", TENANT),)
DEPOT = {"lat": 12.90, "lon": 77.50}


@pytest.fixture()
def stub():
    srv, port = server.make_server("127.0.0.1:0")
    srv.start()
    chan = grpc.insecure_channel(f"127.0.0.1:{port}")
    yield routing_pb2_grpc.RoutingServiceStub(chan)
    chan.close()
    srv.stop(0)


def vehicle(i, cap):
    return {"id": f"v{i}", "capacity": cap, "start": DEPOT}


def inc(i, dlat, dlon, demand=1, urgency=3, **extra):
    return {"id": f"i{i}", "lat": DEPOT["lat"] + dlat, "lon": DEPOT["lon"] + dlon, "demand": demand, "urgency": urgency, **extra}


def optimise(stub, doc, meta=AUTH):
    out = stub.Optimise(routing_pb2.RoutingRequest(payload=json.dumps(doc).encode()), metadata=meta)
    return json.loads(out.payload)


def block(stub, hid, polygon):
    out = stub.BlockEdges(common_pb2.HazardPolygon(payload=json.dumps({"id": hid, "kind": "flood", "polygon": polygon}).encode()), metadata=AUTH)
    return json.loads(out.payload)


def served(plan):
    return [s for r in plan["routes"] for s in r["stops"]]


def test_every_incident_is_served_once_within_capacity(stub):
    incidents = [inc(i, 0.002 * i, 0.001 * i, demand=3) for i in range(1, 5)]
    plan = optimise(stub, {"vehicles": [vehicle(1, 6), vehicle(2, 6)], "incidents": incidents})
    assert sorted(served(plan)) == ["i1", "i2", "i3", "i4"] and plan["unassigned"] == []
    for r in plan["routes"]:
        assert sum(3 for _ in r["stops"]) <= 6 and r["travel_ms"] >= 0
    assert {r["vehicle_id"] for r in plan["routes"]} == {"v1", "v2"}


def test_when_capacity_runs_out_the_most_urgent_incident_is_served(stub):
    plan = optimise(stub, {"vehicles": [vehicle(1, 1)], "incidents": [inc(1, 0.01, 0, urgency=1), inc(2, 0.012, 0, urgency=5)]})
    assert served(plan) == ["i2"] and plan["unassigned"] == ["i1"]


def test_an_unreachable_time_window_leaves_the_incident_unassigned(stub):
    far = inc(1, 0.5, 0.5, window_start_ms=0, window_end_ms=10_000)           # ~70 km away, due in 10 s
    near = inc(2, 0.002, 0, window_start_ms=0, window_end_ms=3_600_000)
    plan = optimise(stub, {"vehicles": [vehicle(1, 5)], "incidents": [far, near]})
    assert plan["unassigned"] == ["i1"] and served(plan) == ["i2"]


def test_a_hazard_blocks_edges_and_the_last_request_is_replanned(stub):
    # depot - B - C in a line, D above and E below. A hazard around B cuts the straight legs through it, so C is reached by the loop depot-D-C-E-depot.
    b, c, d, e = inc(1, 0.0, 0.010), inc(2, 0.0, 0.020), inc(3, 0.012, 0.010), inc(4, -0.012, 0.010)
    request = {"vehicles": [vehicle(1, 9)], "incidents": [b, c, d, e]}
    before = optimise(stub, request)
    assert sorted(served(before)) == ["i1", "i2", "i3", "i4"] and before["unassigned"] == []
    ring = [[77.5085, 12.8985], [77.5115, 12.8985], [77.5115, 12.9015], [77.5085, 12.9015]]   # a box around B, [lon, lat]
    ack = block(stub, "h1", ring)
    assert ack["replanned"] and ack["active_hazards"] == 1 and ack["cells"]
    after = ack["plan"]
    assert after["unassigned"] == ["i1"]                       # B is inside the hazard
    assert sorted(served(after)) == ["i2", "i3", "i4"]
    where = {i["id"]: (i["lon"], i["lat"]) for i in request["incidents"]}
    for r in after["routes"]:                                  # no leg of any route crosses the hazard box
        legs = [(DEPOT["lon"], DEPOT["lat"])] + [where[s] for s in r["stops"]] + [(DEPOT["lon"], DEPOT["lat"])]
        for a, b in zip(legs, legs[1:]):
            assert not server.vrp.blocked_edges([a, b], [[tuple(p) for p in ring]]), f"leg {a} -> {b} crosses the hazard"
    assert optimise(stub, request) == after                    # a new Optimise keeps applying the hazard


def test_the_same_hazard_twice_changes_nothing(stub):
    optimise(stub, {"vehicles": [vehicle(1, 5)], "incidents": [inc(1, 0, 0.010)]})
    poly = [[77.5085, 12.8985], [77.5115, 12.8985], [77.5115, 12.9015], [77.5085, 12.9015]]
    first, second = block(stub, "h1", poly), block(stub, "h1", poly)
    assert first["active_hazards"] == second["active_hazards"] == 1 and first["plan"] == second["plan"]


def test_hazards_are_per_tenant(stub):
    optimise(stub, {"vehicles": [vehicle(1, 5)], "incidents": [inc(1, 0, 0.010)]})
    block(stub, "h1", [[77.5085, 12.8985], [77.5115, 12.8985], [77.5115, 12.9015], [77.5085, 12.9015]])
    other = (("x-tenant-id", "22222222-2222-4222-8222-222222222222"),)
    plan = optimise(stub, {"vehicles": [vehicle(1, 5)], "incidents": [inc(1, 0, 0.010)]}, meta=other)
    assert plan["unassigned"] == [] and served(plan) == ["i1"]


def test_blocked_cells_in_the_request_are_applied(stub):
    cell = h3.str_to_int(h3.latlng_to_cell(DEPOT["lat"], DEPOT["lon"] + 0.010, 9))
    plan = optimise(stub, {"vehicles": [vehicle(1, 5)], "incidents": [inc(1, 0, 0.010), inc(2, 0.01, 0)], "blocked_cells": [cell]})
    assert plan["unassigned"] == ["i1"] and served(plan) == ["i2"]


def test_hazard_cells_cover_the_polygon():
    poly = [(77.50, 12.90), (77.52, 12.90), (77.52, 12.92), (77.50, 12.92)]
    cells = server.hazard_cells(poly)
    assert cells and all(h3.is_valid_cell(h3.int_to_str(c)) for c in cells)
    lat, lon = h3.cell_to_latlng(h3.int_to_str(cells[0]))
    assert 12.90 <= lat <= 12.92 and 77.50 <= lon <= 77.52


@pytest.mark.parametrize("doc,why", [
    ({"vehicles": [], "incidents": []}, "no vehicles"),
    ({"vehicles": [vehicle(1, 5)], "incidents": [{"id": "a", "lat": 99, "lon": 0}]}, "latitude out of range"),
    ({"vehicles": [vehicle(1, 5)], "incidents": [inc(1, 0, 0.01), inc(1, 0, 0.02)]}, "duplicate incident id"),
    ({"vehicles": [vehicle(1, 5)], "incidents": [inc(1, 0, 0.01, urgency=9)]}, "urgency out of range"),
    ({"vehicles": [vehicle(1, 5), {"id": "v2", "capacity": 5, "start": {"lat": 13.5, "lon": 77.5}}], "incidents": []}, "different depots"),
    ({"vehicles": [vehicle(1, 5)], "incidents": [], "blocked_cells": ["abc"]}, "bad cell"),
    ({"vehicles": [vehicle(1, 5)], "incidents": [inc(1, 0, 0.01)], "blocked_cells": [12345]}, "invalid h3 cell"),
])
def test_bad_requests_are_refused(stub, doc, why):
    with pytest.raises(grpc.RpcError) as e:
        optimise(stub, doc)
    assert e.value.code() == grpc.StatusCode.INVALID_ARGUMENT, why


def test_credentials_json_and_hazard_checks(stub):
    req = routing_pb2.RoutingRequest(payload=b"{}")
    with pytest.raises(grpc.RpcError) as e:
        stub.Optimise(req)
    assert e.value.code() == grpc.StatusCode.UNAUTHENTICATED
    with pytest.raises(grpc.RpcError) as e:
        stub.Optimise(routing_pb2.RoutingRequest(payload=b"not json"), metadata=AUTH)
    assert e.value.code() == grpc.StatusCode.INVALID_ARGUMENT
    with pytest.raises(grpc.RpcError) as e:
        stub.Optimise(routing_pb2.RoutingRequest(tenant_id="other", payload=b"{}"), metadata=AUTH)
    assert e.value.code() == grpc.StatusCode.INVALID_ARGUMENT
    for bad in ({"id": "h", "polygon": [[0, 0], [1, 1]]}, {"polygon": [[0, 0], [1, 0], [1, 1]]}, {"id": "h", "polygon": [[0, 0], [1, 0], [200, 1]]}):
        with pytest.raises(grpc.RpcError) as e:
            stub.BlockEdges(common_pb2.HazardPolygon(payload=json.dumps(bad).encode()), metadata=AUTH)
        assert e.value.code() == grpc.StatusCode.INVALID_ARGUMENT
