#!/usr/bin/env python3
"""The load generator and the judge of the zero-drop chaos test.

  writer.py send   : report incidents to the dispatch service at a steady rate while the script kills things; an incident counts as ACCEPTED only
                     when the service acknowledged it (OK, or ALREADY_EXISTS for a retry of one whose first answer was lost). Accepted ids are
                     appended to a file the moment they are acknowledged, so a crash of the writer itself loses nothing.
  writer.py chain  : ask the service to verify the tenant's hash chain.
  writer.py verify : compare the accepted ids with what the database and the Redpanda topic hold afterwards. Exit 1 if any accepted id is missing.
"""
from __future__ import annotations

import argparse
import json
import os
import pathlib
import sys
import time
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "gen" / "py"))

try:
    import h3
except ImportError:  # the cell is only stored, never interpreted by the service
    h3 = None

PERMANENT = {"INVALID_ARGUMENT", "UNAUTHENTICATED", "PERMISSION_DENIED", "NOT_FOUND", "FAILED_PRECONDITION", "UNIMPLEMENTED"}


def make_incident(run_id: str, i: int) -> dict:
    """The i-th incident of a run. The id is derived from the run and the index, so a retry (or a rerun) sends the same incident."""
    lat, lon = 12.90 + (i % 200) * 0.0005, 77.50 + (i % 300) * 0.0004
    cell = int(h3.latlng_to_cell(lat, lon, 9), 16) if h3 else 0x8960145B487FFFF
    return {"id": str(uuid.uuid5(uuid.NAMESPACE_URL, f"{run_id}/{i}")), "kind": "flood", "severity": ("red", "yellow", "green")[i % 3],
            "lat": lat, "lon": lon, "h3_cell": cell, "reported_at": 1_700_000_000_000 + i}


def send_one(call, incident: dict, deadline_s: float, sleep=time.sleep, clock=time.monotonic):
    """Reports one incident, retrying through outages until deadline_s. Returns 'ok', 'duplicate' (an earlier attempt had been committed) or
    None (never acknowledged). A permanent refusal raises, because retrying cannot help and silently skipping would hide a bug."""
    import grpc
    end, pause = clock() + deadline_s, 0.05
    while True:
        try:
            call(json.dumps(incident).encode())
            return "ok"
        except grpc.RpcError as e:
            name = e.code().name
            if name == "ALREADY_EXISTS":
                return "duplicate"
            if name in PERMANENT:
                raise RuntimeError(f"the service refused incident {incident['id']}: {name} {e.details()}") from e
        if clock() >= end:
            return None
        sleep(pause)
        pause = min(pause * 2, 1.0)


def run_writer(addr: str, tenant: str, count: int, rate: float, out: pathlib.Path, run_id: str, deadline_s: float = 60.0, log=print) -> dict:
    import grpc
    from aegismesh.v1 import dispatch_pb2, dispatch_pb2_grpc
    chan = grpc.insecure_channel(addr)
    stub = dispatch_pb2_grpc.DispatchServiceStub(chan)
    meta = (("x-tenant-id", tenant), ("x-actor-id", "chaos-writer"))
    stats = {"sent": 0, "accepted": 0, "duplicates": 0, "unacknowledged": 0}
    start = time.monotonic()
    with out.open("a") as f:
        for i in range(count):
            wait = start + i / rate - time.monotonic()
            if wait > 0:
                time.sleep(wait)
            inc = make_incident(run_id, i)
            stats["sent"] += 1
            r = send_one(lambda payload: stub.ReportIncident(dispatch_pb2.IncidentReport(payload=payload), metadata=meta, timeout=5), inc, deadline_s)
            if r is None:
                stats["unacknowledged"] += 1
                log(f"incident {i} was never acknowledged", file=sys.stderr)
                continue
            stats["accepted"] += 1
            stats["duplicates"] += r == "duplicate"
            f.write(inc["id"] + "\n")
            f.flush()
            os.fsync(f.fileno())
    chan.close()
    return stats


def verify_chain(addr: str, tenant: str) -> dict:
    import grpc
    from aegismesh.v1 import dispatch_pb2, dispatch_pb2_grpc
    with grpc.insecure_channel(addr) as chan:
        reply = dispatch_pb2_grpc.DispatchServiceStub(chan).VerifyChain(dispatch_pb2.VerifyRequest(), metadata=(("x-tenant-id", tenant), ("x-actor-id", "chaos-writer")), timeout=30)
    return json.loads(reply.payload)


# ---------------------------------------------------------------------------------------------------------------------------- judging
def read_ids(path: pathlib.Path) -> set[str]:
    return {l.strip() for l in path.read_text().splitlines() if l.strip()}


def parse_topic(text: str) -> set[str]:
    """Incident ids in the messages `rpk topic consume --format '%v\\n'` printed: one JSON event per line. Lines that are not events are ignored."""
    ids = set()
    for line in text.splitlines():
        try:
            ev = json.loads(line)
        except ValueError:
            continue
        if isinstance(ev, dict) and ev.get("event_type") == "IncidentReported" and isinstance(ev.get("payload"), dict) and ev["payload"].get("id"):
            ids.add(ev["payload"]["id"])
    return ids


def judge(accepted: set[str], in_db: set[str], in_topic: set[str], chain: dict | None = None) -> tuple[bool, list[str]]:
    """(passed, report lines). Every accepted write must be in the database AND on the topic; the hash chain must verify."""
    problems = []
    if not accepted:
        problems.append("no write was accepted: the test proved nothing")
    for name, have in (("the database", in_db), ("the Redpanda topic", in_topic)):
        missing = sorted(accepted - have)
        if missing:
            problems.append(f"{len(missing)} accepted write(s) missing from {name}, for example {missing[:3]}")
    if chain is not None and not chain.get("ok"):
        problems.append(f"the hash chain does not verify: {chain}")
    summary = [f"accepted {len(accepted)}, in database {len(accepted & in_db)}, on topic {len(accepted & in_topic)}"]
    return (not problems), summary + problems


def main(argv=None) -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("send")
    s.add_argument("--addr", default="127.0.0.1:50052")
    s.add_argument("--tenant", required=True)
    s.add_argument("--count", type=int, default=3000)
    s.add_argument("--rate", type=float, default=200.0)
    s.add_argument("--out", type=pathlib.Path, required=True)
    s.add_argument("--run-id", default=str(uuid.uuid4()))
    s.add_argument("--deadline", type=float, default=60.0)
    c = sub.add_parser("chain")
    c.add_argument("--addr", default="127.0.0.1:50052")
    c.add_argument("--tenant", required=True)
    v = sub.add_parser("verify")
    v.add_argument("--accepted", type=pathlib.Path, required=True)
    v.add_argument("--db", type=pathlib.Path, required=True, help="file with one incident id per line, as stored in event_store")
    v.add_argument("--topic", type=pathlib.Path, required=True, help="file with the messages consumed from the topic, one JSON event per line")
    v.add_argument("--chain", type=pathlib.Path, help="file with the VerifyChain reply as JSON")
    a = p.parse_args(argv)
    if a.cmd == "send":
        stats = run_writer(a.addr, a.tenant, a.count, a.rate, a.out, a.run_id, a.deadline)
        print(json.dumps(stats))
        return 0 if stats["accepted"] > 0 else 1
    if a.cmd == "chain":
        r = verify_chain(a.addr, a.tenant)
        print(json.dumps(r))
        return 0 if r.get("ok") else 1
    ok, lines = judge(read_ids(a.accepted), read_ids(a.db), parse_topic(a.topic.read_text()), json.loads(a.chain.read_text()) if a.chain else None)
    print("\n".join(lines))
    print("ZERO DROP: PASS" if ok else "ZERO DROP: FAIL")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
