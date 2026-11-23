"""Vehicle routing with OR-Tools: capacities, optional time windows, urgency (higher = costlier to leave unserved), blocked edges (hazards).
Needs `ortools`. Node 0 is the depot. `matrix[i][j]` is travel time (integer seconds) from i to j."""
from __future__ import annotations

from ortools.constraint_solver import pywrapcp, routing_enums_pb2

BLOCKED = 10_000_000


def segments_cross(p1, p2, p3, p4) -> bool:
    def o(a, b, c):
        v = (b[0] - a[0]) * (c[1] - a[1]) - (b[1] - a[1]) * (c[0] - a[0])
        return (v > 0) - (v < 0)
    return o(p1, p2, p3) != o(p1, p2, p4) and o(p3, p4, p1) != o(p3, p4, p2)


def point_in_polygon(pt, poly) -> bool:
    x, y, inside = pt[0], pt[1], False
    for i in range(len(poly)):
        (x1, y1), (x2, y2) = poly[i], poly[(i + 1) % len(poly)]
        if (y1 > y) != (y2 > y) and x < (x2 - x1) * (y - y1) / (y2 - y1) + x1:
            inside = not inside
    return inside


def blocked_edges(points: list[tuple[float, float]], polygons: list[list[tuple[float, float]]]) -> set[tuple[int, int]]:
    """Edges (i, j) whose straight line enters a hazard polygon: an endpoint inside it, or the line crossing one of its sides."""
    out = set()
    for i, a in enumerate(points):
        for j, b in enumerate(points):
            if i == j:
                continue
            for poly in polygons:
                sides = [(poly[k], poly[(k + 1) % len(poly)]) for k in range(len(poly))]
                if point_in_polygon(a, poly) or point_in_polygon(b, poly) or any(segments_cross(a, b, s, e) for s, e in sides):
                    out.add((i, j)); break
    return out


def solve(matrix, demands, capacities, urgency=None, time_windows=None, service=None, blocked=frozenset(), time_limit_s: int = 2, horizon: int = 86_400):
    n = len(matrix)
    mgr = pywrapcp.RoutingIndexManager(n, len(capacities), 0)
    routing = pywrapcp.RoutingModel(mgr)
    cost = [[BLOCKED if (i, j) in blocked else int(matrix[i][j]) for j in range(n)] for i in range(n)]
    t = routing.RegisterTransitCallback(lambda a, b: cost[mgr.IndexToNode(a)][mgr.IndexToNode(b)])
    routing.SetArcCostEvaluatorOfAllVehicles(t)
    d = routing.RegisterUnaryTransitCallback(lambda a: demands[mgr.IndexToNode(a)])
    routing.AddDimensionWithVehicleCapacity(d, 0, list(capacities), True, "load")
    if time_windows:
        tt = routing.RegisterTransitCallback(lambda a, b: cost[mgr.IndexToNode(a)][mgr.IndexToNode(b)] + (service[mgr.IndexToNode(a)] if service else 0))
        routing.AddDimension(tt, horizon, horizon, False, "time")
        dim = routing.GetDimensionOrDie("time")
        for node, (lo, hi) in enumerate(time_windows):
            if node:
                dim.CumulVar(mgr.NodeToIndex(node)).SetRange(lo, hi)
    for node in range(1, n):          # any job may be dropped, at a price: the more urgent, the higher
        routing.AddDisjunction([mgr.NodeToIndex(node)], int((urgency[node] if urgency else 1) * 100_000))
    p = pywrapcp.DefaultRoutingSearchParameters()
    p.first_solution_strategy = routing_enums_pb2.FirstSolutionStrategy.PATH_CHEAPEST_ARC
    p.local_search_metaheuristic = routing_enums_pb2.LocalSearchMetaheuristic.GUIDED_LOCAL_SEARCH
    p.time_limit.seconds = time_limit_s
    sol = routing.SolveWithParameters(p)
    if sol is None:
        return {"routes": [], "unserved": list(range(1, n))}
    routes, served = [], set()
    for v in range(len(capacities)):
        i, r = routing.Start(v), []
        while not routing.IsEnd(i):
            r.append(mgr.IndexToNode(i)); i = sol.Value(routing.NextVar(i))
        r.append(0)
        routes.append(r); served.update(r)
    return {"routes": routes, "unserved": [k for k in range(1, n) if k not in served]}
