"""The stack check's parsing and judging, without Docker: what counts as a broken service, a missing table, a leaky tenant."""
import json
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))

import stack_check as sc  # noqa: E402

OK = {"A_incidents": "1", "A_sees_B": "0", "A_units": "1", "B_incidents": "1", "B_sees_A": "0", "B_units": "0", "no_tenant_incidents": "0"}


def test_compose_ps_is_read_in_both_formats():
    objs = [{"Service": "postgres", "State": "running", "Health": "healthy"}, {"Service": "redis", "State": "running", "Health": ""}]
    assert sc.parse_ps("\n".join(json.dumps(o) for o in objs)) == objs
    assert sc.parse_ps(json.dumps(objs)) == objs
    assert sc.parse_ps("  \n") == []


def test_services_that_are_missing_exited_or_unhealthy_are_named():
    ps = [{"Service": "postgres", "State": "running", "Health": "healthy"}, {"Service": "envoy", "State": "exited", "ExitCode": 1},
          {"Service": "redis", "State": "running", "Health": "unhealthy"}, {"Service": "vault", "State": "running", "Health": ""}]
    problems = sc.service_problems(["postgres", "envoy", "redis", "vault", "keycloak"], ps)
    assert len(problems) == 3
    assert any(p.startswith("envoy: exited (exit code 1)") for p in problems)
    assert any(p.startswith("redis: running but unhealthy") for p in problems)
    assert any(p.startswith("keycloak: no container") for p in problems)
    assert sc.service_problems(["postgres", "vault"], ps) == []


def test_the_expected_schema_is_read_from_the_real_migrations():
    want = sc.expected_schema([f.read_text() for f in sorted(sc.MIGRATIONS.glob("*.sql"))])
    assert {"tenants", "incidents", "event_store"} <= set(want["tables"])
    assert "event_store" in want["rls"] and "event_store" in want["forced"] and "tenants" not in want["rls"]
    assert "postgis" in want["extensions"]


def test_schema_problems_name_what_is_missing_or_not_forced():
    want = {"tables": ["a", "b", "c"], "rls": ["a", "b"], "forced": ["a", "b"], "extensions": ["postgis", "vector"]}
    problems = sc.schema_problems(want, {"a": (True, True), "b": (True, False)}, {"postgis"})
    assert problems == ["table c does not exist", "table b: row-level security is not forced (the table owner would bypass it)", "extension vector is not loaded"]
    assert sc.schema_problems(want, {"a": (True, True), "b": (True, True), "c": (False, False)}, {"postgis", "vector"}) == []


def test_a_leaky_tenant_probe_is_reported_check_by_check():
    assert sc.rls_problems(OK, "ERROR: new row violates row-level security policy for table incidents") == []
    leaky = {**OK, "A_sees_B": "1", "no_tenant_incidents": "2"}
    problems = sc.rls_problems(leaky, "ERROR: new row violates row-level security policy")
    assert problems == ["A_sees_B: expected 0, got 1", "no_tenant_incidents: expected 0, got 2"]
    assert "NOT refused" in " ".join(sc.rls_problems(OK, "INSERT 0 1"))
    assert "got nothing" in " ".join(sc.rls_problems({}, ""))


def test_the_probe_runs_in_a_transaction_that_is_rolled_back_as_a_non_superuser():
    p = sc.PROBE.strip()
    assert p.startswith("begin;") and p.endswith("rollback;")
    assert "nosuperuser" in p and "set local role rls_probe" in p
