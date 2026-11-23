"""Duplicate-report detection: the same event reported twice. Two reports are duplicates when they are in the same H3 cell or a touching one,
close in time, and their embeddings point the same way (cosine similarity)."""
from __future__ import annotations

import math
from dataclasses import dataclass

import h3

WINDOW_MS = 10 * 60 * 1000       # reports further apart than this are different events
THRESHOLD = 0.90                 # cosine similarity at or above which two reports are the same event
KEEP_MS = 60 * 60 * 1000         # how long a report stays in the index
MAX_PER_CELL = 1000
MAX_DIMS = 4096


def cosine(a: list[float], b: list[float]) -> float:
    """Cosine similarity of two vectors of the same length; 0.0 if either has no direction."""
    if len(a) != len(b):
        raise ValueError("vectors differ in length")
    dot = sum(x * y for x, y in zip(a, b))
    na, nb = math.sqrt(sum(x * x for x in a)), math.sqrt(sum(y * y for y in b))
    return 0.0 if na == 0 or nb == 0 else max(-1.0, min(1.0, dot / (na * nb)))


def check_embedding(v) -> list[float]:
    if not isinstance(v, list) or not 1 <= len(v) <= MAX_DIMS:
        raise ValueError(f"embedding must be a list of 1 to {MAX_DIMS} numbers")
    out = []
    for x in v:
        if isinstance(x, bool) or not isinstance(x, (int, float)) or not math.isfinite(x):
            raise ValueError("embedding values must be finite numbers")
        out.append(float(x))
    if not any(out):
        raise ValueError("embedding must not be all zeros")
    return out


@dataclass(frozen=True)
class Report:
    id: str
    cell: str            # h3 cell, string form
    at_ms: int
    embedding: tuple


def cell_str(cell: int) -> str:
    c = h3.int_to_str(cell) if isinstance(cell, int) else None
    if c is None or not h3.is_valid_cell(c):
        raise ValueError("h3_cell must be a valid h3 cell integer")
    return c


class DuplicateIndex:
    """Recent reports by tenant and cell. A report that is not a duplicate is remembered; a duplicate is not (it adds nothing new)."""

    def __init__(self):
        self._cells: dict[str, dict[str, list[Report]]] = {}

    def find(self, tenant: str, report_id: str, cell: int, at_ms: int, embedding: list[float]) -> tuple[str | None, float]:
        """(id of the best matching earlier report or None, its similarity). Does not change the index."""
        c = cell_str(cell)
        by_cell = self._cells.get(tenant, {})
        best_id, best = None, 0.0
        for near in h3.grid_disk(c, 1):
            for r in by_cell.get(near, []):
                if r.id == report_id or abs(r.at_ms - at_ms) > WINDOW_MS or len(r.embedding) != len(embedding):
                    continue
                sim = cosine(list(r.embedding), embedding)
                if sim > best:
                    best_id, best = r.id, sim
        return (best_id, best) if best >= THRESHOLD else (None, best)

    def add(self, tenant: str, report_id: str, cell: int, at_ms: int, embedding: list[float]) -> None:
        c = cell_str(cell)
        bucket = self._cells.setdefault(tenant, {}).setdefault(c, [])
        if any(r.id == report_id for r in bucket):
            return
        bucket.append(Report(report_id, c, at_ms, tuple(embedding)))
        bucket[:] = [r for r in bucket if at_ms - r.at_ms <= KEEP_MS][-MAX_PER_CELL:]
