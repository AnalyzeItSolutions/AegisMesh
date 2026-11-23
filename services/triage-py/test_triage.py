"""TriageService over a real gRPC connection, plus the duplicate index and the confidence rules on their own."""
import json

import grpc
import h3
import pytest

import dedupe
import server
from aegismesh.v1 import triage_pb2, triage_pb2_grpc

A = "11111111-1111-4111-8111-111111111111"
B = "22222222-2222-4222-8222-222222222222"
HERE_CELL = h3.str_to_int(h3.latlng_to_cell(12.97, 77.59, 9))
T0 = 1_700_000_000_000


class Rig:
    def __init__(self, **kw):
        self.service = server.TriageService(**kw)
        self.srv, port = server.make_server("127.0.0.1:0", self.service)
        self.srv.start()
        self.chan = grpc.insecure_channel(f"127.0.0.1:{port}")
        self.stub = triage_pb2_grpc.TriageServiceStub(self.chan)

    def classify(self, doc, tenant=A):
        out = self.stub.Classify(triage_pb2.TriageInput(payload=json.dumps(doc).encode()), metadata=(("x-tenant-id", tenant),))
        return json.loads(out.payload)

    def dup(self, incident, embedding, tenant=A):
        doc = {"incident": incident, "embedding": embedding}
        out = self.stub.FindDuplicate(triage_pb2.TriageInput(payload=json.dumps(doc).encode()), metadata=(("x-tenant-id", tenant),))
        return json.loads(out.payload)

    def close(self):
        self.chan.close()
        self.srv.stop(0)


@pytest.fixture()
def rig():
    r = Rig()
    yield r
    r.close()


def inc(i, at=T0, cell=HERE_CELL):
    return {"id": f"inc-{i}", "kind": "flood", "lat": 12.97, "lon": 77.59, "h3_cell": cell, "reported_at": at}


FULL = {"can_walk": False, "breathing": True, "respirations_per_min": 20, "radial_pulse": True, "obeys_commands": True}


@pytest.mark.parametrize("patch,colour", [
    ({"can_walk": True}, "green"),
    ({"breathing": False}, "black"),
    ({"breathing": False, "breathing_after_airway": True}, "red"),
    ({"respirations_per_min": 31}, "red"),
    ({"respirations_per_min": 30}, "yellow"),
    ({"radial_pulse": False}, "red"),
    ({"radial_pulse": None, "cap_refill_s": 3}, "red"),
    ({"obeys_commands": False}, "red"),
    ({}, "yellow"),
])
def test_start_colours_over_the_wire(rig, patch, colour):
    assert rig.classify({**FULL, **patch})["colour"] == colour


def test_confidence_follows_what_was_observed(rig):
    assert rig.classify(FULL) == {"colour": "yellow", "confidence": 0.95, "needs_review": False}
    assert rig.classify({"respirations_per_min": 40})["confidence"] == 0.95          # a positive finding decides on its own
    thin = rig.classify({"can_walk": False})                                          # nothing observed: yellow only by default
    assert thin["colour"] == "yellow" and thin["confidence"] == 0.35 and thin["needs_review"]
    assert rig.classify({"breathing": False, "breathing_after_airway": False})["confidence"] == 0.9
    unsure = rig.classify({"breathing": False})                                       # airway not tried: black is a guess
    assert unsure["colour"] == "black" and unsure["needs_review"]


def test_unsure_cases_land_in_the_review_queue_for_their_tenant_only(rig):
    out = rig.classify({"can_walk": False})
    assert out["review_id"] == "r1"
    rig.classify(FULL)                                                                # confident: not queued
    q = rig.service.review_queue(A)
    assert [r["review_id"] for r in q] == ["r1"] and q[0]["colour"] == "yellow" and q[0]["observation"] == {"can_walk": False}
    assert rig.service.review_queue(B) == []


def test_a_model_is_consulted_only_when_the_flag_is_on_and_the_tree_is_unsure():
    calls = []

    def opinion(doc):
        calls.append(doc)
        return "red", 0.8

    off = Rig(second_opinion=opinion)                                                 # flag off by default
    try:
        assert off.classify({"can_walk": False})["colour"] == "yellow" and calls == []
    finally:
        off.close()
    on = Rig(second_opinion=opinion, model_enabled=True)
    try:
        out = on.classify({"can_walk": False})
        assert out["colour"] == "red" and out["confidence"] == 0.8 and not out["needs_review"] and len(calls) == 1
        on.classify(FULL)
        assert len(calls) == 1                                                        # a confident tree answer is never second-guessed
    finally:
        on.close()


def test_a_failing_or_silly_model_never_changes_the_tree_answer():
    def boom(doc):
        raise RuntimeError("model down")
    for op in (boom, lambda d: ("purple", 0.99), lambda d: ("red", 0.1)):
        r = Rig(second_opinion=op, model_enabled=True)
        try:
            out = r.classify({"can_walk": False})
            assert out["colour"] == "yellow" and out["needs_review"]
        finally:
            r.close()


@pytest.mark.parametrize("doc", [
    {"breathing": "yes"}, {"respirations_per_min": 500}, {"respirations_per_min": True}, {"cap_refill_s": -1}, {"surprise": 1}, {"can_walk": 1},
])
def test_bad_observations_are_refused(rig, doc):
    with pytest.raises(grpc.RpcError) as e:
        rig.classify(doc)
    assert e.value.code() == grpc.StatusCode.INVALID_ARGUMENT


def test_credentials_json_and_rate_limit_are_enforced(rig):
    with pytest.raises(grpc.RpcError) as e:
        rig.stub.Classify(triage_pb2.TriageInput(payload=b"{}"))
    assert e.value.code() == grpc.StatusCode.UNAUTHENTICATED
    with pytest.raises(grpc.RpcError) as e:
        rig.stub.Classify(triage_pb2.TriageInput(payload=b"nope"), metadata=(("x-tenant-id", A),))
    assert e.value.code() == grpc.StatusCode.INVALID_ARGUMENT
    with pytest.raises(grpc.RpcError) as e:
        rig.stub.Classify(triage_pb2.TriageInput(tenant_id=B, payload=b"{}"), metadata=(("x-tenant-id", A),))
    assert e.value.code() == grpc.StatusCode.INVALID_ARGUMENT


def test_the_rate_limit_is_per_tenant():
    now = [0.0]
    r = Rig(rate=1.0, burst=2.0, clock=lambda: now[0])
    try:
        r.classify(FULL), r.classify(FULL)
        with pytest.raises(grpc.RpcError) as e:
            r.classify(FULL)
        assert e.value.code() == grpc.StatusCode.RESOURCE_EXHAUSTED
        assert r.classify(FULL, tenant=B)["colour"] == "yellow"                       # tenant B has its own bucket
        now[0] = 5.0                                                                  # time passes: tenant A may call again
        assert r.classify(FULL)["colour"] == "yellow"
    finally:
        r.close()


def test_the_same_event_reported_twice_is_found(rig):
    emb = [1.0, 0.2, 0.0, 0.5]
    assert rig.dup(inc(1), emb) == {"duplicate_of": None, "similarity": 0.0}
    out = rig.dup(inc(2, at=T0 + 5 * 60_000), [0.9, 0.25, 0.0, 0.5])
    assert out["duplicate_of"] == "inc-1" and out["similarity"] >= 0.9


@pytest.mark.parametrize("name,second,emb", [
    ("different place", inc(2, cell=h3.str_to_int(h3.latlng_to_cell(28.61, 77.21, 9))), [1.0, 0.2, 0.0, 0.5]),
    ("too long after", inc(2, at=T0 + 11 * 60_000), [1.0, 0.2, 0.0, 0.5]),
    ("different description", inc(2), [0.0, 0.0, 1.0, -0.5]),
    ("other embedding size", inc(2), [1.0, 0.2]),
])
def test_reports_that_differ_are_not_duplicates(rig, name, second, emb):
    rig.dup(inc(1), [1.0, 0.2, 0.0, 0.5])
    assert rig.dup(second, emb)["duplicate_of"] is None, name


def test_a_touching_cell_counts_as_the_same_place(rig):
    neighbour = [c for c in h3.grid_disk(h3.int_to_str(HERE_CELL), 1) if h3.str_to_int(c) != HERE_CELL][0]
    rig.dup(inc(1), [1.0, 0.0])
    assert rig.dup(inc(2, cell=h3.str_to_int(neighbour)), [1.0, 0.0])["duplicate_of"] == "inc-1"


def test_duplicates_never_cross_tenants_or_match_themselves(rig):
    rig.dup(inc(1), [1.0, 0.0])
    assert rig.dup(inc(2), [1.0, 0.0], tenant=B)["duplicate_of"] is None            # another tenant's report is invisible
    assert rig.dup(inc(1), [1.0, 0.0])["duplicate_of"] is None                       # the same id again is not a duplicate of itself


def test_the_best_match_wins(rig):
    rig.dup(inc(1), [1.0, 0.3])
    rig.dup(inc(2, at=T0 + 60_000), [0.0, 1.0])
    out = rig.dup(inc(3, at=T0 + 120_000), [1.0, 0.31])
    assert out["duplicate_of"] == "inc-1"


@pytest.mark.parametrize("doc", [
    {"embedding": [1.0]}, {"incident": {"id": "x"}, "embedding": [1.0]}, {"incident": inc(1), "embedding": []}, {"incident": inc(1), "embedding": [0, 0]},
    {"incident": inc(1), "embedding": ["a"]}, {"incident": {**inc(1), "h3_cell": 12345}, "embedding": [1.0]}, {"incident": {**inc(1), "reported_at": -1}, "embedding": [1.0]},
])
def test_bad_duplicate_requests_are_refused(rig, doc):
    with pytest.raises(grpc.RpcError) as e:
        rig.stub.FindDuplicate(triage_pb2.TriageInput(payload=json.dumps(doc).encode()), metadata=(("x-tenant-id", A),))
    assert e.value.code() == grpc.StatusCode.INVALID_ARGUMENT


def test_cosine_basics():
    assert dedupe.cosine([1, 0], [1, 0]) == pytest.approx(1.0)
    assert dedupe.cosine([1, 0], [0, 1]) == pytest.approx(0.0)
    assert dedupe.cosine([1, 0], [-1, 0]) == pytest.approx(-1.0)
    assert dedupe.cosine([0, 0], [1, 1]) == 0.0
    with pytest.raises(ValueError):
        dedupe.cosine([1], [1, 2])
