"""H3 cell helpers for viewport subscriptions and de-duplication (needs the `h3` package, version 4)."""
from __future__ import annotations

import h3


def cell_for(lat: float, lon: float, res: int = 8) -> str:
    return h3.latlng_to_cell(lat, lon, res)


def ring(cell: str, k: int = 1) -> set[str]:
    return set(h3.grid_disk(cell, k))


def cells_in_bbox(min_lat: float, min_lon: float, max_lat: float, max_lon: float, res: int = 7) -> set[str]:
    """Every cell whose centre lies in the box: the cells a map viewport must subscribe to."""
    poly = h3.LatLngPoly([(min_lat, min_lon), (min_lat, max_lon), (max_lat, max_lon), (max_lat, min_lon)])
    return set(h3.polygon_to_cells(poly, res))


def same_place(a: tuple[float, float], b: tuple[float, float], res: int = 9) -> bool:
    """Two points in the same cell or touching cells: a cheap first test for 'the same event reported twice' before comparing text."""
    ca, cb = cell_for(*a, res), cell_for(*b, res)
    return ca == cb or cb in ring(ca, 1)
