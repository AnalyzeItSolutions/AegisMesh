"""The hazard consumer against the real routing server over gRPC, with fake brokers and a fake blocker for failure cases."""
import json
import sys
import threading
import time

import grpc
import pytest

pytest.importorskip("ortools")

import consumer as cons  # noqa: E402
import server  # noqa: E402
from aegismesh.v1 import routing_pb2, routing_pb2_grpc  # noqa: E402

A = "11111111-1111-4111-8111-111111111111"
B = "22222222-2222-4222-8222-222222222222"
DEPOT = {"lat": 12.90, "lon": 77.50}
BOX = [[77.5085, 12.8985], [77.5115, 12.8985], [77.5115, 12.9015], [77.5085, 12.9015]]     # around a stop 0.01 deg east of the depot
REQUEST = {"vehicles": [{"id": "v1", "capacity": 5, "start": DEPOT}],
           "incidents": [{"id": "near", "lat": 12.90, "lon": 77.510, "demand": 1, "urgency": 3}, {"id": "up", "lat": 12.91, "lon": 77.50, "demand": 1, "urgency": 3}]}


def event(event_type, tenant=A, payload=None, offset=0):
    body = {"tenant_id": tenant, "sequence_id": offset + 1, "aggregate_id": "cccccccc-0000-4000-8000-000000000001", "event_type": event_type,
            "payload": payload if payload is not None else {}, "actor_id": "u", "prev_hash": "0" * 64, "entry_hash": "1" * 64}
    return cons.Message(cons.TOPIC, body["aggregate_id"], json.dumps(body).encode(), 0, offset)


def hazard(hid="h1", polygon=BOX, tenant=A, offset=0):
    return event(cons.HAZARD_EVENT, tenant, {"id": hid, "kind": "flood", "polygon": polygon}, offset)


@pytest.fixture()
def live():
    srv, port = server.make_server("127.0.0.1:0")
    srv.start()
    chan = grpc.insecure_channel(f"127.0.0.1:{port}")
    stub = routing_pb2_grpc.RoutingServiceStub(chan)
    yield stub
    chan.close()
    srv.stop(0)


def optimise(stub, tenant):
    out = stub.Optimise(routing_pb2.RoutingRequest(payload=json.dumps(REQUEST).encode()), metadata=(("x-tenant-id", tenant),))
    return json.loads(out.payload)


def test_a_hazard_event_blocks_edges_for_its_tenant_only(live):
    assert optimise(live, A)["unassigned"] == [] and optimise(live, B)["unassigned"] == []
    fc = cons.FakeConsumer([event("IncidentReported", A, {"id": "x"}, 0), hazard(offset=1), event("RoadBlocked", A, {"edge_id": "e"}, 2)])
    hc = cons.HazardConsumer(fc, cons.GrpcBlocker(live))
    assert hc.run_once(0) == 3
    assert hc.stats == {"applied": 1, "ignored": 2, "skipped": 0}
    assert [m.offset for m in fc.committed] == [0, 1, 2]
    assert optimise(live, A)["unassigned"] == ["near"]          # the hazard is in force for tenant A
    assert optimise(live, B)["unassigned"] == []                # and not for tenant B


def test_a_redelivered_event_changes_nothing(live):
    optimise(live, A)
    hc = cons.HazardConsumer(cons.FakeConsumer([hazard(offset=1), hazard(offset=1)]), cons.GrpcBlocker(live))
    hc.run_once(0)
    ack = cons.GrpcBlocker(live).block(A, {"id": "h1", "kind": "flood", "polygon": BOX})
    assert ack["active_hazards"] == 1 and hc.stats["applied"] == 2


def test_messages_that_can_never_succeed_are_skipped_and_do_not_block_the_rest(live):
    optimise(live, A)
    bad = [cons.Message(cons.TOPIC, "k", b"not json", 0, 0),
           cons.Message(cons.TOPIC, "k", b'["list"]', 0, 1),
           event(cons.HAZARD_EVENT, "not-a-uuid", {"id": "h", "polygon": BOX}, 2),
           event(cons.HAZARD_EVENT, A, {"kind": "flood"}, 3),
           hazard("tiny", [[0, 0], [1, 1]], offset=4)]                # the server refuses a two-point polygon as invalid
    fc = cons.FakeConsumer(bad + [hazard("ok", offset=5)])
    hc = cons.HazardConsumer(fc, cons.GrpcBlocker(live))
    assert hc.run_once(0) == 6
    assert hc.stats == {"applied": 1, "ignored": 0, "skipped": 5}
    assert len(fc.committed) == 6 and optimise(live, A)["unassigned"] == ["near"]


class Flaky:
    def __init__(self, failures):
        self.failures, self.calls = failures, []

    def block(self, tenant, hazard_doc):
        self.calls.append((tenant, hazard_doc["id"]))
        if self.failures > 0:
            self.failures -= 1
            raise cons.TransientError("UNAVAILABLE: routing is restarting")
        return {}


def test_a_transient_failure_is_retried_with_growing_pauses():
    pauses, flaky = [], Flaky(2)
    hc = cons.HazardConsumer(cons.FakeConsumer([hazard()]), flaky, sleep=pauses.append, attempts=5, backoff_s=0.5)
    assert hc.run_once(0) == 1 and hc.stats["applied"] == 1
    assert len(flaky.calls) == 3 and pauses == [0.5, 1.0]


def test_when_retries_run_out_only_the_handled_prefix_is_committed():
    fc, flaky = cons.FakeConsumer([hazard("h1", offset=0), hazard("h2", offset=1), hazard("h3", offset=2)]), Flaky(0)
    original = flaky.block

    def block(tenant, doc):
        if doc["id"] == "h2":
            raise cons.TransientError("UNAVAILABLE")
        return original(tenant, doc)
    flaky.block = block
    hc = cons.HazardConsumer(fc, flaky, sleep=lambda s: None, attempts=3)
    with pytest.raises(cons.TransientError):
        hc.run_once(0)
    assert [m.offset for m in fc.committed] == [0]               # h2 and h3 stay uncommitted, so Kafka redelivers them


def test_grpc_failures_are_sorted_into_permanent_and_transient():
    class Boom(grpc.RpcError):
        def __init__(self, code, details):
            self._c, self._d = code, details

        def code(self):
            return self._c

        def details(self):
            return self._d

    class Stub:
        def __init__(self, err):
            self.err = err

        def BlockEdges(self, *a, **k):
            raise self.err

    with pytest.raises(cons.PermanentError):
        cons.GrpcBlocker(Stub(Boom(grpc.StatusCode.INVALID_ARGUMENT, "bad polygon"))).block(A, {"id": "h"})
    with pytest.raises(cons.TransientError):
        cons.GrpcBlocker(Stub(Boom(grpc.StatusCode.UNAVAILABLE, "down"))).block(A, {"id": "h"})
    assert cons.GrpcBlocker(Stub(Boom(grpc.StatusCode.FAILED_PRECONDITION, "stale plan"))).block(A, {"id": "h"})["replanned"] is False


def test_run_loop_stops_on_the_event_and_closes_the_consumer():
    fc = cons.FakeConsumer()
    stop = threading.Event()
    t = threading.Thread(target=cons.HazardConsumer(fc, Flaky(0)).run, args=(stop, 0.01))
    t.start()
    time.sleep(0.05)
    stop.set()
    t.join(2)
    assert not t.is_alive() and fc.closed


def test_the_kafka_consumer_says_what_is_missing(monkeypatch):
    monkeypatch.setitem(sys.modules, "confluent_kafka", None)
    with pytest.raises(RuntimeError, match="confluent-kafka"):
        cons.KafkaConsumer("localhost:9092")
