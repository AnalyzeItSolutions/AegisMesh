#!/usr/bin/env python3
"""Tries to break the gateway (Envoy + Keycloak) in front of the real telemetry service. Needs `docker compose up -d --wait keycloak envoy`.

Every service trusts the x-tenant-id header, so tenant isolation is exactly as strong as the gateway's refusal to let a client set it. Checks:
  health needs no token; a call with no token, a garbage token or a token with a damaged signature is UNAUTHENTICATED (gRPC) / 401 (HTTP);
  a valid token reaches the service as ITS tenant, over gRPC and over the browser's HTTP API (/api/sync/*, /api/events); the other tenant cannot see that data; a client-supplied x-tenant-id is ignored;
  a WebSocket needs a token (as ?access_token=, since browsers cannot set headers there), is refused without one, and delivers live frames only to the
  tenant that owns them; CORS allows the web app's origin and no other.

Starts the telemetry, sync and dispatch services itself (Go binaries on the host, with in-memory stores) unless something already listens on their ports. Exit 0 all passed,
1 something failed, 3 it could not run (never a pass). Standard library plus grpcio and the generated stubs.
"""
from __future__ import annotations

import base64
import http.client
import json
import os
import pathlib
import socket
import struct
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "gen" / "py"))

GATEWAY = ("localhost", 8080)
KEYCLOAK = "http://localhost:8081/realms/app/protocol/openid-connect/token"
SERVICES = [("telemetry-go", 50054, 8084), ("sync-go", 50051, 8051), ("dispatch-go", 50052, 8052)]   # (directory under services/, gRPC port, HTTP port)
ORIGIN = "http://localhost:3000"
TENANT_A, TENANT_B = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
LAT, LON = 12.97, 77.59


def needs(what: str) -> None:
    print(f"NEEDS: {what} (the gateway check cannot run without it; this is not a pass)", file=sys.stderr)
    sys.exit(3)


def token(user: str) -> str:
    body = urllib.parse.urlencode({"client_id": "web", "grant_type": "password", "username": user, "password": f"{user}-dev"}).encode()
    try:
        with urllib.request.urlopen(urllib.request.Request(KEYCLOAK, data=body), timeout=15) as r:
            return json.load(r)["access_token"]
    except (urllib.error.URLError, OSError, KeyError) as e:
        needs(f"Keycloak at {KEYCLOAK} to hand out tokens ({e}); run: docker compose up -d --wait keycloak envoy")
        raise


def damaged(tok: str) -> str:
    """The same token with its signature changed: it must not be accepted."""
    head, payload, sig = tok.split(".")
    return ".".join([head, payload, ("A" if sig[0] != "A" else "B") + sig[1:]])


def listening(port: int) -> bool:
    with socket.socket() as s:
        s.settimeout(0.3)
        return s.connect_ex(("127.0.0.1", port)) == 0


def http_get(path: str, headers: dict | None = None, method: str = "GET", body: str | None = None) -> tuple[int, dict, str]:
    c = http.client.HTTPConnection(*GATEWAY, timeout=10)
    c.request(method, path, body=body, headers=headers or {})
    r = c.getresponse()
    out = (r.status, {k.lower(): v for k, v in r.getheaders()}, r.read().decode("utf-8", "replace"))
    c.close()
    return out


# ---------------------------------------------------------------------------------------------------------------------------- a minimal WebSocket client
def ws_frame(text: str) -> bytes:
    """A masked client text frame."""
    data = text.encode()
    mask = os.urandom(4)
    n = len(data)
    head = bytes([0x81]) + (bytes([0x80 | n]) if n < 126 else bytes([0x80 | 126]) + struct.pack(">H", n))
    return head + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(data))


def read_exact(sock: socket.socket, n: int) -> bytes:
    buf = b""
    while len(buf) < n:
        chunk = sock.recv(n - len(buf))
        if not chunk:
            raise ConnectionError("closed")
        buf += chunk
    return buf


def ws_read(sock: socket.socket) -> str | None:
    """The next text message from the server, or None for a close frame."""
    b0, b1 = read_exact(sock, 2)
    n = b1 & 0x7F
    if n == 126:
        n = struct.unpack(">H", read_exact(sock, 2))[0]
    elif n == 127:
        n = struct.unpack(">Q", read_exact(sock, 8))[0]
    data = read_exact(sock, n)
    return data.decode() if (b0 & 0x0F) == 1 else None


def ws_connect(path: str, headers: dict | None = None) -> tuple[int, socket.socket | None]:
    """(status, socket). The socket is open and upgraded only when the status is 101."""
    s = socket.create_connection(GATEWAY, timeout=10)
    lines = [f"GET {path} HTTP/1.1", f"Host: {GATEWAY[0]}:{GATEWAY[1]}", "Upgrade: websocket", "Connection: Upgrade",
             f"Sec-WebSocket-Key: {base64.b64encode(os.urandom(16)).decode()}", "Sec-WebSocket-Version: 13", f"Origin: {ORIGIN}"]
    lines += [f"{k}: {v}" for k, v in (headers or {}).items()]
    s.sendall(("\r\n".join(lines) + "\r\n\r\n").encode())
    buf = b""
    while b"\r\n\r\n" not in buf:
        buf += s.recv(4096)
    status = int(buf.split(b" ", 2)[1])
    if status != 101:
        s.close()
        return status, None
    return status, s


# ---------------------------------------------------------------------------------------------------------------------------- the checks
def grpc_calls(addr: str):
    import grpc
    from aegismesh.v1 import telemetry_pb2, telemetry_pb2_grpc
    stub = telemetry_pb2_grpc.TelemetryServiceStub(grpc.insecure_channel(addr))

    def ingest(tok: str | None, unit: str, extra: dict | None = None):
        md = ([("authorization", f"Bearer {tok}")] if tok else []) + list((extra or {}).items())
        return stub.Ingest(telemetry_pb2.LocationFrame(payload=json.dumps({"unit_id": unit, "lat": LAT, "lon": LON}).encode()), metadata=md, timeout=10)

    def snapshot(tok: str) -> set[str]:
        import h3
        cell = h3.str_to_int(h3.latlng_to_cell(LAT, LON, 7))
        out = stub.Subscribe(telemetry_pb2.ViewportRequest(payload=json.dumps({"cells": [cell]}).encode()), metadata=[("authorization", f"Bearer {tok}")], timeout=10)
        return {f["unit_id"] for f in json.loads(out.payload)["frames"]}
    return grpc, ingest, snapshot


def run_checks() -> list[tuple[str, str | None]]:
    """[(name, None if it passed else why)]"""
    results: list[tuple[str, str | None]] = []

    def check(name: str, fn) -> None:
        try:
            why = fn()
        except Exception as e:  # noqa: BLE001 - a crashed check is a failed check, with its reason
            why = f"{type(e).__name__}: {e}"
        results.append((name, why))

    alice, bob = token("alice"), token("bob")
    grpc, ingest, snapshot = grpc_calls(f"{GATEWAY[0]}:{GATEWAY[1]}")
    run = str(int(time.time()))

    def refused(call) -> str | None:
        try:
            call()
        except grpc.RpcError as e:
            return None if e.code() == grpc.StatusCode.UNAUTHENTICATED else f"expected UNAUTHENTICATED, got {e.code().name}"
        return "the call was accepted"

    def health():
        status, _, body = http_get("/healthz")
        return None if (status, body) == (200, "ok") else f"{status} {body!r}"

    check("health answers without a token", health)
    check("a gRPC call with no token is refused", lambda: refused(lambda: ingest(None, "x")))
    check("a gRPC call with a garbage token is refused", lambda: refused(lambda: ingest("not.a.token", "x")))
    check("a gRPC call with a damaged signature is refused", lambda: refused(lambda: ingest(damaged(alice), "x")))

    def own_data():
        ingest(alice, f"a-{run}")
        seen = snapshot(alice)
        return None if f"a-{run}" in seen else f"alice does not see her own unit: {sorted(seen)[:5]}"

    def isolation():
        return None if f"a-{run}" not in snapshot(bob) else "bob can see alice's unit"

    def spoof():
        ingest(bob, f"spoof-{run}", extra={"x-tenant-id": TENANT_A, "x-actor-id": "root"})     # bob claims to be tenant A
        if f"spoof-{run}" in snapshot(alice):
            return "a client-supplied x-tenant-id put bob's data into alice's tenant"
        return None if f"spoof-{run}" in snapshot(bob) else "bob's own data is missing"

    carol = token("carol")          # a real user of the realm who belongs to no tenant: her token has no tenant_id claim

    def no_tenant_forges_one():
        # nothing overwrites x-tenant-id when the token has no tenant claim, so only the gateway REMOVING the client's header keeps this out
        try:
            ingest(carol, f"forged-{run}", extra={"x-tenant-id": TENANT_A})
        except grpc.RpcError as e:
            return None if e.code() == grpc.StatusCode.UNAUTHENTICATED else f"expected UNAUTHENTICATED, got {e.code().name}"
        return "a valid token with no tenant, plus a forged x-tenant-id, wrote into someone's tenant"

    check("a valid token reaches the service as its own tenant", own_data)
    check("a valid token with no tenant cannot act, even with a forged x-tenant-id", no_tenant_forges_one)
    check("the other tenant cannot see that data", isolation)
    check("a client-supplied x-tenant-id is ignored", spoof)

    def ws_no_token():
        status, sock = ws_connect("/ws")
        if sock:
            sock.close()
        return None if status == 401 else f"expected 401, got {status}"

    def ws_header_spoof():
        status, sock = ws_connect("/ws", {"x-tenant-id": TENANT_A})
        if sock:
            sock.close()
        return None if status == 401 else f"a spoofed header without a token got {status}"

    def ws_live():
        status, sa = ws_connect(f"/ws?access_token={alice}")
        if not sa:
            return f"alice's upgrade got {status}"
        status, sb = ws_connect(f"/ws?access_token={bob}")
        if not sb:
            sa.close()
            return f"bob's upgrade got {status}"
        try:
            import h3
            viewport = json.dumps({"type": "viewport", "cells": [h3.str_to_int(h3.latlng_to_cell(LAT, LON, 7))]})
            for s in (sa, sb):
                s.sendall(ws_frame(viewport))
                first = json.loads(ws_read(s) or "{}")
                if first.get("type") != "snapshot":
                    return f"expected a snapshot first, got {first}"
            unit = f"live-{run}"
            ingest(alice, unit)
            sa.settimeout(5)
            msg = json.loads(ws_read(sa) or "{}")
            if msg.get("frame", {}).get("unit_id") != unit:
                return f"alice did not receive her live frame: {msg}"
            sb.settimeout(1.5)
            try:
                other = ws_read(sb)
                return f"bob received alice's live frame: {other}"
            except (TimeoutError, socket.timeout):
                return None
        finally:
            sa.close()
            sb.close()

    check("a WebSocket without a token is refused", ws_no_token)
    check("a WebSocket with only a spoofed tenant header is refused", ws_header_spoof)
    check("a WebSocket with a token streams live frames to its own tenant only", ws_live)

    # ---- the browser-facing HTTP API: /api/sync/* (field app) and /api/events (time travel)
    def api(method, path, tok, body=None, extra=None):
        h = {"content-type": "application/json", **({"authorization": f"Bearer {tok}"} if tok else {}), **(extra or {})}
        status, headers, text = http_get(path, h, method, body)
        return status, text, headers                              # (status, body, headers): the order the checks below unpack

    batch = lambda key: json.dumps({"replica_id": "r-" + run, "lww": [{"key": key, "value": "v", "hlc": 7340032, "replica_id": "r-" + run}]})

    def api_needs_a_token():
        for method, path, body in (("POST", "/api/sync/push", batch("k")), ("POST", "/api/sync/pull", "{}"), ("GET", "/api/events", None)):
            status, _, _ = api(method, path, None, body)
            if status != 401:
                return f"{method} {path} without a token got {status}"
        status, _, _ = api("GET", "/api/events", damaged(alice))
        return None if status == 401 else f"a damaged token got {status}"

    def sync_over_http():
        key = f"incident:{run}"
        status, out, _ = api("POST", "/api/sync/push", alice, batch(key))
        if status != 200 or '"accepted":1' not in out.replace(" ", ""):
            return f"push as alice: {status} {out[:120]}"
        status, out, _ = api("POST", "/api/sync/pull", alice, json.dumps({"since_seq": 0}))
        if status != 200 or key not in out:
            return f"alice's pull does not return her own write: {status} {out[:160]}"
        status, out, _ = api("POST", "/api/sync/pull", bob, json.dumps({"since_seq": 0}))
        if status != 200 or key in out:
            return f"bob's pull shows alice's data (or failed): {status} {out[:160]}"
        return None

    def sync_forged_tenant():
        key = f"forged-{run}"
        status, _, _ = api("POST", "/api/sync/push", bob, batch(key), extra={"x-tenant-id": TENANT_A})
        if status != 200:
            return f"push as bob: {status}"
        _, out, _ = api("POST", "/api/sync/pull", alice, "{}")
        if key in out:
            return "a forged x-tenant-id put bob's write into alice's tenant"
        _, out, _ = api("POST", "/api/sync/pull", bob, "{}")
        return None if key in out else "bob's own write is missing"

    def events_over_http():
        from aegismesh.v1 import dispatch_pb2, dispatch_pb2_grpc
        stub = dispatch_pb2_grpc.DispatchServiceStub(grpc.insecure_channel(f"{GATEWAY[0]}:{GATEWAY[1]}"))
        incident = {"id": "aaaaaaaa-0000-4000-8000-" + run.zfill(12)[-12:], "kind": "flood", "severity": "red", "lat": LAT, "lon": LON, "h3_cell": 618683397703204863, "reported_at": 1700000000000}
        stub.ReportIncident(dispatch_pb2.IncidentReport(payload=json.dumps(incident).encode()), metadata=[("authorization", f"Bearer {alice}")], timeout=10)
        status, out, hdr = api("GET", "/api/events?after=0&limit=100", alice)
        if status != 200 or incident["id"] not in out:
            return f"alice's event log: {status} {out[:200]}"
        if '"h3_cell":618683397703204863' not in out.replace(" ", ""):
            return "the 64-bit cell id did not survive as exact digits"
        status, out, _ = api("GET", "/api/events?after=0", bob)
        return None if status == 200 and incident["id"] not in out else f"bob sees alice's events (or failed): {status} {out[:120]}"

    def api_cors():
        status, h, _ = http_get("/api/sync/push", {"Origin": ORIGIN, "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "authorization,content-type"}, method="OPTIONS")
        ok = status in (200, 204) and h.get("access-control-allow-origin") == ORIGIN and "post" in h.get("access-control-allow-methods", "").lower()
        return None if ok else f"{status} {h.get('access-control-allow-origin')!r} {h.get('access-control-allow-methods')!r}"

    check("the HTTP API refuses a missing or damaged token", api_needs_a_token)
    check("field sync over HTTP: a write is pulled back by its tenant and not by another", sync_over_http)
    check("field sync over HTTP: a forged x-tenant-id cannot move a write into another tenant", sync_forged_tenant)
    check("the event log over HTTP: exact cell ids, own tenant only", events_over_http)
    check("CORS preflight for the sync API from the web app's origin", api_cors)

    def cors_allowed():
        status, h, _ = http_get("/ws", {"Origin": ORIGIN, "Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "authorization"}, method="OPTIONS")
        ok = status in (200, 204) and h.get("access-control-allow-origin") == ORIGIN and "authorization" in h.get("access-control-allow-headers", "")
        return None if ok else f"{status} {h.get('access-control-allow-origin')!r}"

    def cors_denied():
        status, h, _ = http_get("/ws", {"Origin": "https://evil.example", "Access-Control-Request-Method": "GET"}, method="OPTIONS")
        return None if "access-control-allow-origin" not in h else f"an unknown origin was allowed: {h['access-control-allow-origin']}"

    check("CORS allows the web app's origin for a preflight", cors_allowed)
    check("CORS does not allow another origin", cors_denied)
    return results


def main() -> int:
    for port, what in ((8080, "Envoy"), (8081, "Keycloak")):
        if not listening(port):
            needs(f"{what} on port {port}: run docker compose up -d --wait keycloak envoy")
    try:
        import grpc  # noqa: F401
        import h3  # noqa: F401
        from aegismesh.v1 import telemetry_pb2_grpc  # noqa: F401
    except Exception as e:  # noqa: BLE001
        needs(f"Python with grpcio, h3 and the generated stubs ({type(e).__name__}: {e}); use the project's .venv (make setup)")
    procs, work = [], tempfile.mkdtemp()
    for name, grpc_port, http_port in SERVICES:
        if listening(grpc_port) or listening(http_port):
            if not (listening(grpc_port) and listening(http_port)):
                needs(f"ports {grpc_port} and {http_port} for {name} (one is taken by something else)")
            continue                                               # already running: use it
        build = subprocess.run(["go", "build", "-o", f"{work}/{name}", "."], cwd=ROOT / "services" / name, capture_output=True, text=True)
        if build.returncode != 0:
            print(build.stderr, file=sys.stderr)
            return 1
        env = {k: v for k, v in os.environ.items() if k not in ("REDIS_ADDR", "KAFKA_BROKERS", "DATABASE_URL")}
        env.update(GRPC_ADDR=f":{grpc_port}", HTTP_ADDR=f":{http_port}")
        procs.append(subprocess.Popen([f"{work}/{name}"], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))
        for _ in range(100):
            if listening(grpc_port) and listening(http_port):
                break
            time.sleep(0.1)
    try:
        results = run_checks()
    finally:
        for p in procs:
            p.kill()
            p.wait()
    failed = 0
    for name, why in results:
        print(("PASS " if why is None else "FAIL ") + name + ("" if why is None else f"\n       {why}"))
        failed += why is not None
    print("GATEWAY OK" if not failed else f"GATEWAY NOT OK: {failed} check(s) failed")
    return 0 if not failed else 1


if __name__ == "__main__":
    sys.exit(main())
