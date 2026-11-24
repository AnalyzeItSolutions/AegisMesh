#!/usr/bin/env python3
"""Checks the running docker compose stack. Standard library only.

  1. every service in docker-compose.yml is running (and healthy where it has a health check)
  2. every table the migrations create exists, every table they put under row-level security has it enabled AND forced, the extensions are loaded
  3. row-level security really isolates two tenants, tested as a NON-superuser (a superuser bypasses it) inside a transaction that is rolled back
  4. Redis, Redpanda, Keycloak (its realm was imported), Envoy and Vault answer

Prints one PASS or FAIL line per check and exits 0 only if every check passed. Exit 3: it could not run (no Docker daemon); never a pass.
Run after `docker compose up -d --wait`. Keycloak and Envoy have no health check there, so they are polled for a while.
"""
from __future__ import annotations

import json
import pathlib
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[1]
MIGRATIONS = ROOT / "db" / "migrations"
A, B = "aaaaaaaa-0000-4000-8000-00000000000a", "bbbbbbbb-0000-4000-8000-00000000000b"


def sh(args: list[str], stdin: str | None = None, timeout: int = 90) -> tuple[int, str]:
    p = subprocess.run(args, cwd=ROOT, input=stdin, capture_output=True, text=True, timeout=timeout)
    return p.returncode, (p.stdout + p.stderr)


def compose(*args: str, stdin: str | None = None, timeout: int = 90) -> tuple[int, str]:
    return sh(["docker", "compose", *args], stdin=stdin, timeout=timeout)


def psql(sql: str, db: str = "app") -> tuple[int, str]:
    return compose("exec", "-T", "postgres" if db == "app" else "timescale", "psql", "-U", "postgres", "-d", db, "-v", "ON_ERROR_STOP=0", "-At", stdin=sql)


# ---------------------------------------------------------------------------------------------------------------------------- parsing (testable)
def parse_ps(text: str) -> list[dict]:
    """`docker compose ps --format json`: one JSON object per line (newer) or one JSON array (older)."""
    text = text.strip()
    if not text:
        return []
    if text.startswith("["):
        return json.loads(text)
    return [json.loads(l) for l in text.splitlines() if l.strip().startswith("{")]


def service_problems(expected: list[str], ps: list[dict]) -> list[str]:
    by = {c.get("Service"): c for c in ps}
    out = []
    for name in expected:
        c = by.get(name)
        if c is None:
            out.append(f"{name}: no container (did `docker compose up` run?)")
        elif c.get("State") != "running":
            out.append(f"{name}: {c.get('State')} (exit code {c.get('ExitCode')})")
        elif c.get("Health") not in ("", None, "healthy"):
            out.append(f"{name}: running but {c.get('Health')}")
    return out


def expected_schema(sql_files: list[str]) -> dict:
    sql = "\n".join(sql_files)
    return {"tables": sorted(set(re.findall(r"create table if not exists (\w+)", sql))),
            "rls": sorted(set(re.findall(r"alter table (\w+) enable row level security", sql))),
            "forced": sorted(set(re.findall(r"alter table (\w+) force row level security", sql))),
            "extensions": sorted(set(re.findall(r"create extension if not exists (\w+)", sql)))}


def schema_problems(want: dict, tables: dict[str, tuple[bool, bool]], extensions: set[str]) -> list[str]:
    out = [f"table {t} does not exist" for t in want["tables"] if t not in tables]
    out += [f"table {t}: row-level security is not enabled" for t in want["rls"] if t in tables and not tables[t][0]]
    out += [f"table {t}: row-level security is not forced (the table owner would bypass it)" for t in want["forced"] if t in tables and not tables[t][1]]
    out += [f"extension {e} is not loaded" for e in want["extensions"] if e not in extensions]
    return out


def rls_problems(rows: dict[str, str], output: str) -> list[str]:
    """Judges the isolation probe's results (marker -> value) and its raw output."""
    want = {"A_incidents": "1", "A_sees_B": "0", "A_units": "1", "B_incidents": "1", "B_sees_A": "0", "B_units": "0", "no_tenant_incidents": "0"}
    out = [f"{k}: expected {v}, got {rows.get(k, 'nothing')}" for k, v in want.items() if rows.get(k) != v]
    if "row-level security policy" not in output:
        out.append("a write for another tenant's row was NOT refused by row-level security")
    return out


# ---------------------------------------------------------------------------------------------------------------------------- checks
def check_services() -> list[str]:
    rc, names = compose("config", "--services")
    if rc != 0:
        return [f"docker compose config failed: {names.strip()[-200:]}"]
    expected = [n for n in names.split() if n]
    rc, text = compose("ps", "-a", "--format", "json")
    problems = service_problems(expected, parse_ps(text) if rc == 0 else [])
    for p in problems:
        name = p.split(":")[0]
        if name in expected:
            _, logs = compose("logs", "--tail", "6", "--no-color", name)
            problems[problems.index(p)] = p + "\n      " + "\n      ".join(logs.strip().splitlines()[-6:])
    return problems


def check_schema() -> list[str]:
    want = expected_schema([f.read_text() for f in sorted(MIGRATIONS.glob("*.sql"))])
    rc, out = psql("select c.relname, c.relrowsecurity, c.relforcerowsecurity from pg_class c join pg_namespace n on n.oid = c.relnamespace "
                   "where n.nspname = 'public' and c.relkind = 'r';")
    if rc != 0:
        return [f"could not query the database: {out.strip()[-200:]}"]
    tables = {}
    for line in out.splitlines():
        parts = line.split("|")
        if len(parts) == 3:
            tables[parts[0]] = (parts[1] == "t", parts[2] == "t")
    rc, ext = psql("select extname from pg_extension;")
    return schema_problems(want, tables, set(ext.split()))


PROBE = f"""
begin;
insert into tenants (id, name) values ('{A}', 'probe-a'), ('{B}', 'probe-b');
create role rls_probe nosuperuser nologin;
grant select, insert on all tables in schema public to rls_probe;
set local role rls_probe;
select set_config('app.tenant_id', '{A}', true);
insert into incidents (kind, severity, status, tenant_id) values ('flood', 'red', 'open', '{A}');
insert into units (callsign, status, tenant_id) values ('a-1', 'free', '{A}');
select 'A_incidents', count(*) from incidents;
select 'A_units', count(*) from units;
select set_config('app.tenant_id', '{B}', true);
insert into incidents (kind, severity, status, tenant_id) values ('fire', 'yellow', 'open', '{B}');
select 'B_incidents', count(*) from incidents;
select 'B_units', count(*) from units;
select 'B_sees_A', count(*) from incidents where tenant_id = '{A}';
savepoint cross_write;
insert into incidents (kind, severity, status, tenant_id) values ('forged', 'red', 'open', '{A}');
rollback to savepoint cross_write;
select set_config('app.tenant_id', '{A}', true);
select 'A_sees_B', count(*) from incidents where tenant_id = '{B}';
select set_config('app.tenant_id', '', true);
select 'no_tenant_incidents', count(*) from incidents;
rollback;
"""


def check_rls() -> list[str]:
    rc, out = psql(PROBE)
    rows = {}
    for line in out.splitlines():
        m = re.match(r"^(\w+)\|(\d+)$", line.strip())
        if m:
            rows[m.group(1)] = m.group(2)
    if not rows:
        return [f"the isolation probe produced no results: {out.strip()[-300:]}"]
    return rls_problems(rows, out)


def wait_http(url: str, ok, timeout: int = 180) -> str | None:
    """None when `ok(status, body)` is true before the timeout, else a description of the last failure."""
    end, last = time.monotonic() + timeout, "no answer"
    while time.monotonic() < end:
        try:
            with urllib.request.urlopen(url, timeout=5) as r:
                body = r.read().decode("utf-8", "replace")
                if ok(r.status, body):
                    return None
                last = f"unexpected answer: {r.status} {body[:120]!r}"
        except urllib.error.HTTPError as e:
            last = f"HTTP {e.code}"
        except (urllib.error.URLError, OSError) as e:
            last = str(e)[:120]
        time.sleep(3)
    return f"{url}: {last}"


def check_redis() -> list[str]:
    rc, out = compose("exec", "-T", "redis", "redis-cli", "ping")
    return [] if "PONG" in out else [f"redis did not answer PING: {out.strip()[-120:]}"]


def check_redpanda() -> list[str]:
    rc, out = compose("exec", "-T", "redpanda", "rpk", "cluster", "health")
    return [] if re.search(r"Healthy:\s+true", out) else [f"redpanda is not healthy: {out.strip()[-160:]}"]


def check_timescale() -> list[str]:
    rc, out = psql("select extname from pg_extension;", db="telemetry")
    return [] if "timescaledb" in out.split() else [f"timescaledb is not loaded in the telemetry database: {out.strip()[-160:]}"]


def check_keycloak() -> list[str]:
    err = wait_http("http://localhost:8081/realms/app", lambda s, b: s == 200 and '"realm":"app"' in b.replace(" ", ""))
    return [f"keycloak did not serve the imported realm: {err}"] if err else []


def check_envoy() -> list[str]:
    err = wait_http("http://localhost:9901/ready", lambda s, b: s == 200 and "LIVE" in b, timeout=60)
    return [f"envoy is not ready: {err}"] if err else []


def check_vault() -> list[str]:
    err = wait_http("http://localhost:8200/v1/sys/health", lambda s, b: s == 200 and '"sealed":false' in b.replace(" ", ""), timeout=60)
    return [f"vault is not unsealed: {err}"] if err else []


CHECKS = [("services running and healthy", check_services), ("migrations applied, row-level security on and forced, extensions loaded", check_schema),
          ("two tenants cannot see or forge each other's rows", check_rls), ("redis answers", check_redis), ("redpanda healthy", check_redpanda),
          ("timescaledb loaded", check_timescale), ("keycloak serves the imported realm", check_keycloak), ("envoy ready", check_envoy), ("vault unsealed", check_vault)]


def main() -> int:
    if sh(["docker", "info"], timeout=30)[0] != 0:
        print("NEEDS: a running Docker daemon (this is not a pass)", file=sys.stderr)
        return 3
    failed = 0
    for name, fn in CHECKS:
        try:
            problems = fn()
        except Exception as e:  # noqa: BLE001 - a crashed check is a failed check, with its reason
            problems = [f"the check itself failed: {type(e).__name__}: {e}"]
        if problems:
            failed += 1
            print(f"FAIL {name}")
            for p in problems:
                print(f"   {p}")
        else:
            print(f"PASS {name}")
    print("STACK OK" if not failed else f"STACK NOT OK: {failed} check(s) failed")
    return 0 if not failed else 1


if __name__ == "__main__":
    sys.exit(main())
