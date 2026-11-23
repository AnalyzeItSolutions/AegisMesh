"""START (Simple Triage And Rapid Treatment) decision tree as a pure function: four colours from a few quick observations."""
from __future__ import annotations

from dataclasses import dataclass

GREEN, YELLOW, RED, BLACK = "green", "yellow", "red", "black"


@dataclass(frozen=True)
class Observation:
    can_walk: bool = False
    breathing: bool = True
    breathing_after_airway: bool | None = None     # after repositioning the airway; None = not tried
    resp_rate: int | None = None                    # breaths per minute
    cap_refill_s: float | None = None
    radial_pulse: bool | None = None
    obeys_commands: bool = True


def classify(o: Observation) -> str:
    if o.can_walk:
        return GREEN
    if not o.breathing:
        return RED if o.breathing_after_airway else BLACK
    if o.resp_rate is not None and o.resp_rate > 30:
        return RED
    if (o.radial_pulse is False) or (o.cap_refill_s is not None and o.cap_refill_s > 2):
        return RED
    if not o.obeys_commands:
        return RED
    return YELLOW


ORDER = {RED: 0, YELLOW: 1, GREEN: 2, BLACK: 3}      # treat in this order
