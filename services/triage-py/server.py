"""TriageService over gRPC (payload shapes: CONTRACT.md).

Classify: START colour from quick observations, with a confidence; low confidence goes to a human review queue.
FindDuplicate: is this report the same event as one seen before (same place, close in time, similar embedding)?
A model may give a second opinion on a low-confidence case, but only when TRIAGE_MODEL_ENABLED is set; it is off by default.
"""
from __future__ import annotations

import json
import os
import threading
import time
from concurrent import futures
from typing import Callable

import grpc
from aegismesh.v1 import triage_pb2, triage_pb2_grpc

import dedupe
import start_tiers
import token_bucket

TENANT_HEADER = "x-tenant-id"
REVIEW_BELOW = 0.6
MAX_REVIEW_QUEUE = 1000
OBSERVATION_FIELDS = {"can_walk": bool, "breathing": bool, "breathing_after_airway": bool, "obeys_commands": bool, "radial_pulse": bool,
                      "respirations_per_min": int, "cap_refill_s": (int, float)}
SecondOpinion = Callable[[dict], tuple[str, float]]


def parse_observation(doc: dict) -> start_tiers.Observation:
    """Strict: unknown fields, wrong types and impossible values are refused (ValueError) instead of guessed at."""
    unknown = set(doc) - set(OBSERVATION_FIELDS)
    if unknown:
        raise ValueError(f"unknown observation fields: {', '.join(sorted(unknown))}")
    for k, v in doc.items():
        want = OBSERVATION_FIELDS[k]
        if v is not None and (isinstance(v, bool) != (want is bool) or not isinstance(v, want)):
            raise ValueError(f"{k} has the wrong type")
    rr, cr = doc.get("respirations_per_min"), doc.get("cap_refill_s")
    if rr is not None and not 0 <= rr <= 120:
        raise ValueError("respirations_per_min must be between 0 and 120")
    if cr is not None and not 0 <= cr <= 30:
        raise ValueError("cap_refill_s must be between 0 and 30")
    return start_tiers.Observation(can_walk=doc.get("can_walk", False), breathing=doc.get("breathing", True), breathing_after_airway=doc.get("breathing_after_airway"),
                                   resp_rate=rr, cap_refill_s=cr, radial_pulse=doc.get("radial_pulse"), obeys_commands=doc.get("obeys_commands", True))


def confidence(doc: dict, colour: str) -> float:
    """How sure the tree is, given what was actually observed. A positive finding decides on its own; the default colour (yellow) is only as
    sure as the observations that would have contradicted it, so each missing one costs 0.2; black without an airway attempt is a guess."""
    if colour == start_tiers.GREEN:
        return 0.95 if "can_walk" in doc else 0.6
    if colour == start_tiers.BLACK:
        return 0.9 if doc.get("breathing_after_airway") is False else 0.5
    if colour == start_tiers.RED:
        return 0.95
    missing = sum([doc.get("respirations_per_min") is None, doc.get("radial_pulse") is None and doc.get("cap_refill_s") is None, "obeys_commands" not in doc])
    return round(0.95 - 0.2 * missing, 2)


def classify(doc: dict) -> dict:
    colour = start_tiers.classify(parse_observation(doc))
    conf = confidence(doc, colour)
    return {"colour": colour, "confidence": conf, "needs_review": conf < REVIEW_BELOW}


class _Tenant:
    def __init__(self):
        self.review: list[dict] = []
        self.dups = dedupe.DuplicateIndex()


class TriageService(triage_pb2_grpc.TriageServiceServicer):
    def __init__(self, second_opinion: SecondOpinion | None = None, model_enabled: bool | None = None, rate: float = 50.0, burst: float = 100.0, clock=time.monotonic):
        self._lock = threading.Lock()
        self._tenants: dict[str, _Tenant] = {}
        self._limiter = token_bucket.KeyedLimiter(rate, burst, clock)
        self._second_opinion = second_opinion
        self._model_enabled = os.environ.get("TRIAGE_MODEL_ENABLED", "").lower() in ("1", "true", "yes") if model_enabled is None else model_enabled
        self._seq = 0

    # -- plumbing --
    def _tenant(self, request, context) -> str:
        tenant = dict(context.invocation_metadata()).get(TENANT_HEADER, "")
        if not tenant:
            context.abort(grpc.StatusCode.UNAUTHENTICATED, "missing tenant credentials")
        if request.tenant_id and request.tenant_id != tenant:
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, "tenant_id does not match the credentials")
        with self._lock:
            if not self._limiter.allow(tenant):
                context.abort(grpc.StatusCode.RESOURCE_EXHAUSTED, "too many requests for this tenant")
        return tenant

    def _state(self, tenant: str) -> _Tenant:
        with self._lock:
            return self._tenants.setdefault(tenant, _Tenant())

    @staticmethod
    def _doc(request, context) -> dict:
        try:
            doc = json.loads(request.payload or b"{}")
        except ValueError:
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, "payload is not valid JSON")
        if not isinstance(doc, dict):
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, "payload must be a JSON object")
        return doc

    def review_queue(self, tenant: str) -> list[dict]:
        """The cases waiting for a person, oldest first."""
        with self._lock:
            return list(self._tenants[tenant].review) if tenant in self._tenants else []

    # -- RPCs --
    def Classify(self, request, context):
        tenant = self._tenant(request, context)
        doc = self._doc(request, context)
        try:
            out = classify(doc)
        except ValueError as e:
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, str(e))
        if out["needs_review"] and self._model_enabled and self._second_opinion is not None:
            try:                                              # a model is consulted only when the flag is on and the tree is unsure
                colour, conf = self._second_opinion(doc)
                if colour in start_tiers.ORDER and 0 <= conf <= 1 and conf > out["confidence"]:
                    out = {"colour": colour, "confidence": round(conf, 2), "needs_review": conf < REVIEW_BELOW}
            except Exception:  # noqa: BLE001 - a failing model must not fail triage: the tree's answer stands
                pass
        if out["needs_review"]:
            st = self._state(tenant)
            with self._lock:
                self._seq += 1
                out["review_id"] = f"r{self._seq}"
                st.review.append({"review_id": out["review_id"], "observation": doc, "colour": out["colour"], "confidence": out["confidence"], "at_ms": int(time.time() * 1000)})
                del st.review[:-MAX_REVIEW_QUEUE]
        return triage_pb2.TriageResult(tenant_id=tenant, payload=json.dumps(out).encode())

    def FindDuplicate(self, request, context):
        tenant = self._tenant(request, context)
        doc = self._doc(request, context)
        inc = doc.get("incident")
        try:
            if not isinstance(inc, dict) or not isinstance(inc.get("id"), str) or not inc["id"]:
                raise ValueError("incident needs a string id")
            at = inc.get("reported_at")
            if isinstance(at, bool) or not isinstance(at, int) or at < 0:
                raise ValueError("incident.reported_at must be milliseconds since the epoch")
            cell = inc.get("h3_cell")
            if isinstance(cell, bool) or not isinstance(cell, int):
                raise ValueError("incident.h3_cell must be an h3 cell integer")
            emb = dedupe.check_embedding(doc.get("embedding"))
            st = self._state(tenant)
            with self._lock:
                dup, sim = st.dups.find(tenant, inc["id"], cell, at, emb)
                if dup is None:
                    st.dups.add(tenant, inc["id"], cell, at, emb)
        except ValueError as e:
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, str(e))
        return triage_pb2.DuplicateResult(tenant_id=tenant, payload=json.dumps({"duplicate_of": dup, "similarity": round(sim, 4)}).encode())


def make_server(addr: str = "[::]:50055", service: TriageService | None = None) -> tuple[grpc.Server, int]:
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=8))
    triage_pb2_grpc.add_TriageServiceServicer_to_server(service or TriageService(), server)
    return server, server.add_insecure_port(addr)


def serve() -> None:
    server, port = make_server(os.environ.get("GRPC_ADDR", "[::]:50055"))
    server.start()
    print(f"triage service on port {port}", flush=True)
    server.wait_for_termination()


if __name__ == "__main__":
    serve()
