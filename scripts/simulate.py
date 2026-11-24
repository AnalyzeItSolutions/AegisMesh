#!/usr/bin/env python3
"""Demo traffic: a fleet of rescue units driving around Bengaluru, reported through the gateway as the dev user alice (tenant A), plus a few incidents.

Everything goes the way a real client's does: a Keycloak token, then the gateway's gRPC front (Ingest, ReportIncident). Stop with Ctrl-C.
Usage: python scripts/simulate.py [--units 14] [--seconds 0 (0 = until stopped)] [--gateway localhost:8080]
"""
from __future__ import annotations

import argparse
import json
import math
import pathlib
import random
import sys
import time
import urllib.parse
import urllib.request
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "gen" / "py"))
KEYCLOAK = "http://localhost:8081/realms/app/protocol/openid-connect/token"
CENTRE = (12.9716, 77.5946)


def get_token(user: str) -> tuple[str, float]:
    body = urllib.parse.urlencode({"client_id": "web", "grant_type": "password", "username": user, "password": f"{user}-dev"}).encode()
    with urllib.request.urlopen(urllib.request.Request(KEYCLOAK, data=body), timeout=15) as r:
        d = json.load(r)
    return d["access_token"], time.time() + d["expires_in"] - 30


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--units", type=int, default=14)
    ap.add_argument("--seconds", type=int, default=0)
    ap.add_argument("--gateway", default="localhost:8080")
    a = ap.parse_args()
    try:
        import grpc
        import h3
        from aegismesh.v1 import dispatch_pb2, dispatch_pb2_grpc, telemetry_pb2, telemetry_pb2_grpc
        token, expires = get_token("alice")
    except Exception as e:  # noqa: BLE001
        print(f"NEEDS: grpcio, h3, the generated stubs and a running Keycloak ({type(e).__name__}: {e}); run make setup && make demo", file=sys.stderr)
        return 3
    ch = grpc.insecure_channel(a.gateway)
    tel, dis = telemetry_pb2_grpc.TelemetryServiceStub(ch), dispatch_pb2_grpc.DispatchServiceStub(ch)
    rng = random.Random(7)
    units = [{"id": f"unit-{i + 1:02d}", "lat": CENTRE[0] + rng.uniform(-0.03, 0.03), "lon": CENTRE[1] + rng.uniform(-0.03, 0.03), "h": rng.uniform(0, 360)} for i in range(a.units)]
    started, tick, sent = time.time(), 0, 0
    print(f"{a.units} units around Bengaluru through {a.gateway}; Ctrl-C to stop", flush=True)
    kinds = [("flood", "red"), ("fire", "red"), ("collapse", "yellow"), ("medical", "yellow"), ("road_block", "green")]
    try:
        while not a.seconds or time.time() - started < a.seconds:
            if time.time() > expires:
                token, expires = get_token("alice")
            md = [("authorization", f"Bearer {token}")]
            for u in units:
                u["h"] = (u["h"] + rng.uniform(-25, 25)) % 360
                step = 0.0004
                u["lat"] += step * math.cos(math.radians(u["h"]))
                u["lon"] += step * math.sin(math.radians(u["h"])) / math.cos(math.radians(u["lat"]))
                tel.Ingest(telemetry_pb2.LocationFrame(payload=json.dumps({"unit_id": u["id"], "lat": u["lat"], "lon": u["lon"], "heading": round(u["h"], 1)}).encode()), metadata=md, timeout=10)
                sent += 1
            if tick % 20 == 0 and tick:
                lat, lon = CENTRE[0] + rng.uniform(-0.03, 0.03), CENTRE[1] + rng.uniform(-0.03, 0.03)
                kind, sev = rng.choice(kinds)
                dis.ReportIncident(dispatch_pb2.IncidentReport(payload=json.dumps({"id": str(uuid.uuid4()), "kind": kind, "severity": sev, "lat": lat, "lon": lon, "h3_cell": h3.str_to_int(h3.latlng_to_cell(lat, lon, 7)), "reported_at": int(time.time() * 1000)}).encode()), metadata=md, timeout=10)
                print(f"incident reported: {kind} ({sev})", flush=True)
            tick += 1
            time.sleep(1)
    except KeyboardInterrupt:
        pass
    print(f"stopped after {sent} position reports")
    return 0


if __name__ == "__main__":
    sys.exit(main())
