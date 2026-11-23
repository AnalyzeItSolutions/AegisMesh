"""Behaviour of the generated Python stubs: messages round-trip, every declared service has a client stub, garbage is refused."""
import pathlib
import sys

import pytest

sys.path.insert(0, str(pathlib.Path(__file__).parent))

from google.protobuf.message import DecodeError  # noqa: E402

from aegismesh.v1 import common_pb2, dispatch_pb2, dispatch_pb2_grpc, routing_pb2, routing_pb2_grpc  # noqa: E402
from aegismesh.v1 import sync_pb2, sync_pb2_grpc, telemetry_pb2_grpc, triage_pb2_grpc  # noqa: E402


def test_delta_batch_round_trips_tenant_and_payload():
    m = sync_pb2.DeltaBatch(tenant_id="t1", payload=b'{"replica_id":"r"}')
    back = sync_pb2.DeltaBatch.FromString(m.SerializeToString())
    assert back.tenant_id == "t1" and back.payload == b'{"replica_id":"r"}'


def test_shared_message_is_one_type_across_services():
    # HazardPolygon is used by dispatch AddHazard and routing BlockEdges: both must point at the one message in common.proto
    want = common_pb2.HazardPolygon.DESCRIPTOR.full_name
    add = dispatch_pb2.DESCRIPTOR.services_by_name["DispatchService"].methods_by_name["AddHazard"].input_type.full_name
    block = routing_pb2.DESCRIPTOR.services_by_name["RoutingService"].methods_by_name["BlockEdges"].input_type.full_name
    assert add == block == want


def test_every_service_has_a_client_stub_and_a_servicer():
    for mod, name in ((sync_pb2_grpc, "SyncService"), (dispatch_pb2_grpc, "DispatchService"), (routing_pb2_grpc, "RoutingService"),
                      (telemetry_pb2_grpc, "TelemetryService"), (triage_pb2_grpc, "TriageService")):
        assert hasattr(mod, name + "Stub") and hasattr(mod, name + "Servicer") and hasattr(mod, "add_" + name + "Servicer_to_server")


def test_garbage_bytes_are_refused():
    with pytest.raises(DecodeError):
        sync_pb2.DeltaBatch.FromString(b"\xff\xff\xff\xff\xff")
