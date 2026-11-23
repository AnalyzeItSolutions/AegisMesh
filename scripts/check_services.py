#!/usr/bin/env python3
"""Every gRPC service declared in a .proto file must be registered by hand-written server code, and (with --live) must answer a real call.
Standard library only; copied into the project by the builder.

    check_services.py [--proto proto] [--src .] [--live NAME=HOST:PORT ...]
A service that is declared but never registered is how a project ends up with a server that starts and answers nothing. --live uses `grpcurl`
(when installed) to call the first rpc of the service; "Unimplemented" is a failure, any other answer (even an application error) proves it is wired.
"""
import argparse
import os
import re
import shutil
import subprocess
import sys

SKIP_DIRS = {".git", "node_modules", "target", ".builder", "__pycache__", ".venv", "venv", "dist", "build"}
GENERATED = re.compile(r"(\.pb\.go|_pb2(_grpc)?\.py|_grpc\.pb\.go|\.pb\.ts|_pb\.js|_connect\.ts)$|^Code generated|DO NOT EDIT", re.I)


def walk(root):
    for d, dirs, files in os.walk(root):
        dirs[:] = [x for x in dirs if x not in SKIP_DIRS]
        for f in files:
            yield os.path.join(d, f)


def services(proto_dir):
    out = []
    for p in walk(proto_dir):
        if not p.endswith(".proto"):
            continue
        text = open(p, errors="ignore").read()
        pkg = (re.search(r"^\s*package\s+([\w.]+)\s*;", text, re.M) or [None, ""])[1]
        for m in re.finditer(r"service\s+(\w+)\s*\{(.*?)\n\}", text, re.S):
            rpcs = re.findall(r"rpc\s+(\w+)\s*\(", m.group(2))
            if rpcs:
                out.append((pkg, m.group(1), rpcs, p))
    return out


def registered(src, name):
    pats = [rf"Register{name}Server\b", rf"add_{name}Servicer_to_server\b", rf"{name}Service\b.*addService|addService\(.*{name}", rf"\.add_service\(.*{name}", rf"{name}Server::new|{name}Server\b.*serve"]
    for p in walk(src):
        if not p.endswith((".go", ".py", ".ts", ".js", ".rs", ".java", ".kt")) or GENERATED.search(os.path.basename(p)):
            continue
        text = open(p, errors="ignore").read()
        if GENERATED.search(text[:300]):
            continue
        if any(re.search(x, text) for x in pats):
            return p
    return None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--proto", default="proto"); ap.add_argument("--src", default="."); ap.add_argument("--live", nargs="*", default=[])
    a = ap.parse_args()
    svcs = services(a.proto)
    if not svcs:
        print(f"no services declared under {a.proto}"); return 1
    bad, live = [], dict(x.split("=", 1) for x in a.live if "=" in x)
    for pkg, name, rpcs, path in svcs:
        where = registered(a.src, name)
        print(f"{name}: " + (f"registered in {where}" if where else "NOT REGISTERED by any hand-written server code"))
        if not where:
            bad.append(name); continue
        addr = live.get(name) or live.get(name.lower())
        if addr:
            if not shutil.which("grpcurl"):
                print(f"  live check skipped: grpcurl is not installed"); continue
            r = subprocess.run(["grpcurl", "-plaintext", "-d", "{}", addr, f"{pkg}.{name}/{rpcs[0]}"], capture_output=True, text=True, timeout=30)
            text = r.stdout + r.stderr
            if "Unimplemented" in text or "connection refused" in text.lower() or "failed to dial" in text.lower():
                print(f"  live call {rpcs[0]} FAILED: {text.strip()[:200]}"); bad.append(name)
            else:
                print(f"  live call {rpcs[0]} answered")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
