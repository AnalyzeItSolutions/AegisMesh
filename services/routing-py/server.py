"""RoutingService over gRPC (see CONTRACT.md for the payload shapes).

Optimise plans routes for the vehicles and incidents in the request, applying every hazard polygon the tenant has added.
BlockEdges adds a hazard polygon (the HazardPolygonAdded event) and re-plans the last request so routes stop using edges that cross it.
Coordinates: payloads are GeoJSON order (lon, lat); travel time is straight-line distance at ROUTING_SPEED_KMH.
"""
from __future__ import annotations

import json
import math
import os
import threading
from concurrent import futures

import grpc
import h3
import h3_index
import vrp
from aegismesh.v1 import common_pb2, routing_pb2, routing_pb2_grpc

SPEED_MPS = float(os.environ.get("ROUTING_SPEED_KMH", "40")) / 3.6
TIME_LIMIT_S = int(os.environ.get("ROUTING_TIME_LIMIT_S", "1"))
MAX_VEHICLES, MAX_INCIDENTS, MAX_BLOCKED_CELLS, MAX_HAZARD_CELLS = 200, 1000, 500, 500
TENANT_HEADER = "x-tenant-id"
SAME_DEPOT_M = 50.0


def _meters(a: tuple[float, float], b: tuple[float, float]) -> float:
    """Great-circle distance between two (lon, lat) points."""
    (lo1, la1), (lo2, la2) = a, b
    p1, p2 = math.radians(la1), math.radians(la2)
    h = math.sin((p2 - p1) / 2) ** 2 + math.cos(p1) * math.cos(p2) * math.sin(math.radians(lo2 - lo1) / 2) ** 2
    return 12_742_000 * math.asin(math.sqrt(h))


def _num(v, name: str, lo: float, hi: float) -> float:
    if isinstance(v, bool) or not isinstance(v, (int, float)) or not lo <= v <= hi:
        raise ValueError(f"{name} must be a number between {lo:g} and {hi:g}")
    return float(v)


def _point(o, name: str) -> tuple[float, float]:
    if not isinstance(o, dict):
        raise ValueError(f"{name} needs lat and lon")
    return (_num(o.get("lon"), f"{name}.lon", -180, 180), _num(o.get("lat"), f"{name}.lat", -90, 90))


def parse_polygon(raw, name: str = "polygon") -> list[tuple[float, float]]:
    if not isinstance(raw, list) or len(raw) < 3:
        raise ValueError(f"{name} needs at least 3 [lon, lat] points")
    pts = []
    for p in raw:
        if not isinstance(p, (list, tuple)) or len(p) != 2:
            raise ValueError(f"{name} points are [lon, lat] pairs")
        pts.append((_num(p[0], f"{name} lon", -180, 180), _num(p[1], f"{name} lat", -90, 90)))
    return pts


def cell_polygon(cell: int) -> list[tuple[float, float]]:
    try:
        c = h3.int_to_str(cell)
        if not h3.is_valid_cell(c):
            raise ValueError
        return [(lon, lat) for lat, lon in h3.cell_to_boundary(c)]
    except Exception:  # noqa: BLE001 - any h3 failure means the cell id is bad
        raise ValueError(f"blocked_cells has an invalid h3 cell: {cell}") from None


def hazard_cells(polygon: list[tuple[float, float]]) -> list[int]:
    """The H3 cells (as integers) whose centre lies inside the polygon: what a map or another service needs to show or pass back the hazard.
    Starts at resolution 8 and goes finer for a small hazard; a hazard smaller than any cell still gets the cell of its centre."""
    lons, lats = [p[0] for p in polygon], [p[1] for p in polygon]
    for res in (8, 9, 10):
        out = []
        for c in sorted(h3_index.cells_in_bbox(min(lats), min(lons), max(lats), max(lons), res)):
            lat, lon = h3.cell_to_latlng(c)
            if vrp.point_in_polygon((lon, lat), polygon):
                out.append(h3.str_to_int(c))
        if out:
            return out[:MAX_HAZARD_CELLS]
    return [h3.str_to_int(h3_index.cell_for(sum(lats) / len(lats), sum(lons) / len(lons), 10))]


def plan(req: dict, hazards: list[list[tuple[float, float]]]) -> dict:
    """Routes for the request with every hazard (and the request's own blocked_cells) applied. Raises ValueError on a bad request."""
    vehicles, incidents = req.get("vehicles"), req.get("incidents")
    if not isinstance(vehicles, list) or not vehicles or len(vehicles) > MAX_VEHICLES:
        raise ValueError(f"vehicles must be a list of 1 to {MAX_VEHICLES}")
    if not isinstance(incidents, list) or len(incidents) > MAX_INCIDENTS:
        raise ValueError(f"incidents must be a list of at most {MAX_INCIDENTS}")
    starts = [_point(v.get("start") if isinstance(v, dict) else None, f"vehicles[{i}].start") for i, v in enumerate(vehicles)]
    if any(_meters(starts[0], s) > SAME_DEPOT_M for s in starts):
        raise ValueError("all vehicles must start at the same depot (multi-depot planning is not supported yet)")
    caps = [int(_num(v.get("capacity"), f"vehicles[{i}].capacity", 0, 1_000_000)) for i, v in enumerate(vehicles)]
    ids = []
    pts = [starts[0]]
    demands, urgency, windows, seen = [0], [1], [(0, 0)], set()
    for i, inc in enumerate(incidents):
        if not isinstance(inc, dict) or not isinstance(inc.get("id"), str) or not inc["id"] or inc["id"] in seen:
            raise ValueError(f"incidents[{i}] needs a unique string id")
        seen.add(inc["id"])
        ids.append(inc["id"])
        pts.append(_point(inc, f"incidents[{i}]"))
        demands.append(int(_num(inc.get("demand", 1), f"incidents[{i}].demand", 0, 1_000_000)))
        urgency.append(int(_num(inc.get("urgency", 1), f"incidents[{i}].urgency", 1, 5)))
        windows.append((int(_num(inc.get("window_start_ms", 0), "window_start_ms", 0, 10 ** 12) // 1000),
                        int(_num(inc.get("window_end_ms", 86_400_000), "window_end_ms", 0, 10 ** 12) // 1000)))
    cells = req.get("blocked_cells", [])
    if not isinstance(cells, list) or len(cells) > MAX_BLOCKED_CELLS or any(isinstance(c, bool) or not isinstance(c, int) for c in cells):
        raise ValueError(f"blocked_cells must be a list of at most {MAX_BLOCKED_CELLS} h3 cell integers")
    if not ids:
        return {"routes": [{"vehicle_id": str(v.get("id", i)), "stops": [], "travel_ms": 0} for i, v in enumerate(vehicles)], "unassigned": []}
    n = len(pts)
    matrix = [[0 if i == j else max(1, math.ceil(_meters(pts[i], pts[j]) / SPEED_MPS)) for j in range(n)] for i in range(n)]
    blocked = vrp.blocked_edges(pts, list(hazards) + [cell_polygon(c) for c in cells])
    horizon = max(86_400, max(w[1] for w in windows) + 1)
    use_windows = any(w != (0, 86_400) for w in windows[1:])
    sol = vrp.solve(matrix, demands, caps, urgency=urgency, time_windows=windows if use_windows else None, blocked=blocked,
                    time_limit_s=TIME_LIMIT_S, horizon=horizon)
    routes = []
    for v, r in enumerate(sol["routes"]):
        travel = sum(matrix[a][b] for a, b in zip(r, r[1:]))
        routes.append({"vehicle_id": str(vehicles[v].get("id", v)), "stops": [ids[k - 1] for k in r if k], "travel_ms": travel * 1000})
    return {"routes": routes, "unassigned": [ids[k - 1] for k in sol["unserved"]]}


class _Tenant:
    def __init__(self):
        self.hazards: dict[str, list[tuple[float, float]]] = {}
        self.last_request: dict | None = None
        self.last_plan: dict | None = None


class RoutingService(routing_pb2_grpc.RoutingServiceServicer):
    def __init__(self):
        self._lock = threading.Lock()
        self._tenants: dict[str, _Tenant] = {}

    def _tenant(self, request, context) -> str:
        tenant = dict(context.invocation_metadata()).get(TENANT_HEADER, "")
        if not tenant:
            context.abort(grpc.StatusCode.UNAUTHENTICATED, "missing tenant credentials")
        if request.tenant_id and request.tenant_id != tenant:
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, "tenant_id does not match the credentials")
        return tenant

    def _state(self, tenant: str) -> _Tenant:
        with self._lock:
            return self._tenants.setdefault(tenant, _Tenant())

    @staticmethod
    def _json(request, context) -> dict:
        try:
            doc = json.loads(request.payload or b"{}")
        except ValueError:
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, "payload is not valid JSON")
        if not isinstance(doc, dict):
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, "payload must be a JSON object")
        return doc

    def Optimise(self, request, context):
        tenant = self._tenant(request, context)
        doc = self._json(request, context)
        st = self._state(tenant)
        with self._lock:
            hazards = list(st.hazards.values())
        try:
            result = plan(doc, hazards)
        except ValueError as e:
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, str(e))
        with self._lock:
            st.last_request, st.last_plan = doc, result
        return routing_pb2.RoutingPlan(tenant_id=tenant, payload=json.dumps(result).encode())

    def BlockEdges(self, request, context):
        tenant = self._tenant(request, context)
        doc = self._json(request, context)
        hid = doc.get("id")
        if not isinstance(hid, str) or not hid:
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, "hazard id is required")
        try:
            polygon = parse_polygon(doc.get("polygon"))
        except ValueError as e:
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, str(e))
        st = self._state(tenant)
        with self._lock:                       # the same hazard id again replaces it, so a redelivered event changes nothing
            st.hazards[hid] = polygon
            hazards, last = list(st.hazards.values()), st.last_request
        replanned = None
        if last is not None:
            try:
                replanned = plan(last, hazards)
            except ValueError as e:
                context.abort(grpc.StatusCode.FAILED_PRECONDITION, f"the last plan request is no longer valid: {e}")
            with self._lock:
                st.last_plan = replanned
        ack = {"hazard_id": hid, "active_hazards": len(hazards), "cells": hazard_cells(polygon), "replanned": replanned is not None, "plan": replanned}
        return common_pb2.HazardAck(tenant_id=tenant, payload=json.dumps(ack).encode())


def make_server(addr: str = "[::]:50053") -> tuple[grpc.Server, int]:
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=8))
    routing_pb2_grpc.add_RoutingServiceServicer_to_server(RoutingService(), server)
    port = server.add_insecure_port(addr)
    return server, port


def serve() -> None:
    server, port = make_server(os.environ.get("GRPC_ADDR", "[::]:50053"))
    server.start()
    print(f"routing service on port {port}", flush=True)
    server.wait_for_termination()


if __name__ == "__main__":
    serve()
