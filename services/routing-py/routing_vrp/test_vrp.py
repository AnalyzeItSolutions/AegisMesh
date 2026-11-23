import pytest

pytest.importorskip("ortools")
from vrp import blocked_edges, point_in_polygon, solve

PTS = [(0, 0), (0, 10), (10, 10), (10, 0)]


def grid(points):
    return [[int(((a[0] - b[0]) ** 2 + (a[1] - b[1]) ** 2) ** .5 * 10) for b in points] for a in points]


def test_every_job_served_within_capacity():
    r = solve(grid(PTS), [0, 1, 1, 1], [2, 2])
    served = sorted(n for route in r["routes"] for n in route if n)
    assert served == [1, 2, 3] and r["unserved"] == []


def test_capacity_forces_dropping_the_least_urgent():
    r = solve(grid(PTS), [0, 1, 1, 1], [2], urgency=[0, 1, 5, 9])
    assert sorted(r["unserved"]) == [1] and set(r["routes"][0]) == {0, 2, 3}


def test_hazard_polygon_blocks_edges_and_routes_avoid_them():
    hazard = [(4, 4), (6, 4), (6, 6), (4, 6)]
    assert point_in_polygon((5, 5), hazard) and not point_in_polygon((1, 1), hazard)
    pts = [(0, 0), (10, 10), (0, 10)]
    b = blocked_edges(pts, [hazard])
    assert (0, 1) in b and (1, 0) in b and (0, 2) not in b
    r = solve(grid(pts), [0, 1, 1], [3], blocked=b)
    route = r["routes"][0]
    assert all((route[i], route[i + 1]) not in b for i in range(len(route) - 1))
