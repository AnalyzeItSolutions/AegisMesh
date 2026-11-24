"""Tests of the chaos test's own machinery. The Docker part (Postgres, Redpanda, killing containers) cannot run without a Docker daemon; everything
else is checked here, including the writer against the REAL dispatch service binary, killed and restarted while it is writing."""
import json
import os
import pathlib
import shutil
import signal
import socket
import subprocess
import sys
import threading
import time
import uuid

import grpc
import pytest

HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[1]
sys.path.insert(0, str(HERE))

import writer  # noqa: E402

TENANT = "11111111-1111-4111-8111-111111111111"


# ---------------------------------------------------------------------------------------------------------------------------- the judge
def test_a_run_where_everything_survived_passes_even_with_duplicates_on_the_topic():
    ok, lines = writer.judge({"a", "b"}, {"a", "b", "extra"}, {"a", "b"}, {"ok": True, "checked": 2})
    assert ok and "accepted 2, in database 2, on topic 2" in lines[0]


@pytest.mark.parametrize("db,topic,chain,fragment", [
    ({"a"}, {"a", "b"}, {"ok": True}, "missing from the database"),
    ({"a", "b"}, {"a"}, {"ok": True}, "missing from the Redpanda topic"),
    ({"a", "b"}, {"a", "b"}, {"ok": False, "first_broken_sequence_id": 7}, "hash chain does not verify"),
])
def test_each_kind_of_loss_is_named(db, topic, chain, fragment):
    ok, lines = writer.judge({"a", "b"}, db, topic, chain)
    assert not ok and any(fragment in l for l in lines)


def test_a_run_that_accepted_nothing_proves_nothing_and_fails():
    ok, lines = writer.judge(set(), set(), set(), {"ok": True})
    assert not ok and "proved nothing" in " ".join(lines)


def test_topic_messages_are_read_from_event_lines_and_noise_is_ignored():
    text = "\n".join([
        json.dumps({"event_type": "IncidentReported", "payload": {"id": "i1"}}),
        json.dumps({"event_type": "UnitDispatched", "payload": {"incident_id": "i1", "unit_id": "u"}}),
        json.dumps({"event_type": "IncidentReported", "payload": {}}),
        "Consumed up to offset 4",
        "[1, 2]",
        json.dumps({"event_type": "IncidentReported", "payload": {"id": "i2"}}),
    ])
    assert writer.parse_topic(text) == {"i1", "i2"}


def test_incidents_are_deterministic_unique_and_valid_for_the_service():
    a, b = writer.make_incident("run", 0), writer.make_incident("run", 1)
    assert a == writer.make_incident("run", 0) and a["id"] != b["id"] and a["id"] != writer.make_incident("other", 0)["id"]
    assert uuid.UUID(a["id"]) and a["severity"] in ("red", "yellow", "green") and -90 <= a["lat"] <= 90 and -180 <= a["lon"] <= 180


# ---------------------------------------------------------------------------------------------------------------------------- retries
class Rpc(grpc.RpcError):
    def __init__(self, code, details="x"):
        self._code, self._details = code, details

    def code(self):
        return self._code

    def details(self):
        return self._details


def test_a_write_is_retried_through_an_outage_with_growing_pauses():
    calls, pauses = [], []

    def call(payload):
        calls.append(payload)
        if len(calls) < 4:
            raise Rpc(grpc.StatusCode.UNAVAILABLE)

    now = [0.0]
    assert writer.send_one(call, {"id": "i"}, 60, sleep=lambda s: (pauses.append(s), now.__setitem__(0, now[0] + s)), clock=lambda: now[0]) == "ok"
    assert len(calls) == 4 and pauses == [0.05, 0.1, 0.2]


def test_a_lost_acknowledgement_is_recognised_as_a_commit_not_a_loss():
    assert writer.send_one(lambda p: (_ for _ in ()).throw(Rpc(grpc.StatusCode.ALREADY_EXISTS)), {"id": "i"}, 1) == "duplicate"


def test_a_write_that_never_gets_through_is_reported_unacknowledged_not_accepted():
    now = [0.0]
    r = writer.send_one(lambda p: (_ for _ in ()).throw(Rpc(grpc.StatusCode.UNAVAILABLE)), {"id": "i"}, 2, sleep=lambda s: now.__setitem__(0, now[0] + s), clock=lambda: now[0])
    assert r is None


@pytest.mark.parametrize("code", [grpc.StatusCode.INVALID_ARGUMENT, grpc.StatusCode.UNAUTHENTICATED, grpc.StatusCode.FAILED_PRECONDITION])
def test_a_refusal_is_a_bug_to_surface_not_something_to_retry_forever(code):
    with pytest.raises(RuntimeError, match="refused"):
        writer.send_one(lambda p: (_ for _ in ()).throw(Rpc(code)), {"id": "i"}, 5)


# ---------------------------------------------------------------------------------------------------------------------------- the real service
def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


@pytest.fixture(scope="module")
def dispatch_binary(tmp_path_factory):
    if not shutil.which("go"):
        pytest.skip("go is not installed")
    out = tmp_path_factory.mktemp("bin") / "dispatch"
    r = subprocess.run(["go", "build", "-o", str(out), "."], cwd=ROOT / "services" / "dispatch-go", capture_output=True, text=True)
    assert r.returncode == 0, r.stderr
    return out


class Service:
    """The real dispatch binary with its in-memory store (no DATABASE_URL, no KAFKA_BROKERS)."""

    def __init__(self, binary, port):
        self.binary, self.port, self.proc = binary, port, None

    def start(self):
        env = {k: v for k, v in os.environ.items() if k not in ("DATABASE_URL", "KAFKA_BROKERS")}
        env["GRPC_ADDR"] = f"127.0.0.1:{self.port}"
        self.proc = subprocess.Popen([str(self.binary)], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        for _ in range(100):
            try:
                socket.create_connection(("127.0.0.1", self.port), timeout=0.2).close()
                return self
            except OSError:
                time.sleep(0.1)
        raise AssertionError("the service did not start")

    def kill(self):
        self.proc.send_signal(signal.SIGKILL)
        self.proc.wait()

    @property
    def addr(self):
        return f"127.0.0.1:{self.port}"


def test_the_writer_gets_every_incident_accepted_by_the_real_service_and_the_chain_verifies(dispatch_binary, tmp_path):
    svc = Service(dispatch_binary, free_port()).start()
    try:
        out = tmp_path / "accepted.txt"
        stats = writer.run_writer(svc.addr, TENANT, 60, 600, out, "run-a", log=lambda *a, **k: None)
        assert stats == {"sent": 60, "accepted": 60, "duplicates": 0, "unacknowledged": 0}
        ids = writer.read_ids(out)
        assert len(ids) == 60 and ids == {writer.make_incident("run-a", i)["id"] for i in range(60)}
        assert writer.verify_chain(svc.addr, TENANT) == {"ok": True, "first_broken_sequence_id": None, "checked": 60}
        again = writer.run_writer(svc.addr, TENANT, 60, 600, tmp_path / "again.txt", "run-a", log=lambda *a, **k: None)
        assert again["duplicates"] == 60 and again["accepted"] == 60 and writer.read_ids(tmp_path / "again.txt") == ids, "a rerun of the same incidents is recognised, not stored twice"
        assert writer.verify_chain(svc.addr, TENANT)["checked"] == 60
    finally:
        svc.kill()


def test_the_writer_rides_out_a_killed_service_and_counts_only_what_was_acknowledged(dispatch_binary, tmp_path):
    svc = Service(dispatch_binary, free_port()).start()
    out = tmp_path / "accepted.txt"
    result = {}
    t = threading.Thread(target=lambda: result.update(writer.run_writer(svc.addr, TENANT, 150, 100, out, "run-b", deadline_s=30, log=lambda *a, **k: None)))
    t.start()
    time.sleep(0.6)
    svc.kill()                      # SIGKILL mid-load: no chance to clean up
    time.sleep(1.0)
    svc = Service(dispatch_binary, svc.port).start()
    try:
        t.join(60)
        assert not t.is_alive(), "the writer never finished"
        assert result["sent"] == 150 and result["accepted"] == 150 and result["unacknowledged"] == 0
        assert len(writer.read_ids(out)) == 150, "every accepted id was written to the file as it was acknowledged"
    finally:
        svc.kill()


def test_the_writer_gives_up_on_a_service_that_never_comes_back_and_says_so(dispatch_binary, tmp_path):
    out = tmp_path / "accepted.txt"
    stats = writer.run_writer(f"127.0.0.1:{free_port()}", TENANT, 3, 100, out, "run-c", deadline_s=0.5, log=lambda *a, **k: None)
    assert stats["accepted"] == 0 and stats["unacknowledged"] == 3
    assert not out.read_text()
    assert writer.main(["send", "--addr", f"127.0.0.1:{free_port()}", "--tenant", TENANT, "--count", "1", "--deadline", "0.3", "--out", str(tmp_path / "x.txt")]) == 1


# ---------------------------------------------------------------------------------------------------------------------------- the command-line judge and the script
def test_the_verify_command_exits_non_zero_on_a_lost_write(tmp_path):
    (tmp_path / "a.txt").write_text("i1\ni2\n")
    (tmp_path / "db.txt").write_text("i1\ni2\n")
    (tmp_path / "topic.txt").write_text(json.dumps({"event_type": "IncidentReported", "payload": {"id": "i1"}}) + "\n")
    (tmp_path / "chain.json").write_text('{"ok": true}')
    args = ["verify", "--accepted", str(tmp_path / "a.txt"), "--db", str(tmp_path / "db.txt"), "--topic", str(tmp_path / "topic.txt"), "--chain", str(tmp_path / "chain.json")]
    assert writer.main(args) == 1
    (tmp_path / "topic.txt").write_text("".join(json.dumps({"event_type": "IncidentReported", "payload": {"id": i}}) + "\n" for i in ("i1", "i2")))
    assert writer.main(args) == 0


def test_the_script_is_valid_shell_and_never_reports_a_pass_when_it_cannot_run():
    script = HERE / "zero_drop.sh"
    assert subprocess.run(["bash", "-n", str(script)]).returncode == 0
    env = {**os.environ, "DOCKER_HOST": "unix:///nonexistent/docker.sock"}      # no daemon reachable
    r = subprocess.run(["bash", str(script)], capture_output=True, text=True, env=env, timeout=120)
    assert r.returncode == 3, r.stderr
    assert "NEEDS:" in r.stderr and "not a pass" in r.stderr
