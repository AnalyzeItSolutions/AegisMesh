"""Reads the verified-events topic and tells the routing service about every hazard.

A HazardPolygonAdded event becomes a BlockEdges call for its tenant, which blocks the edges crossing the polygon and re-plans.
Delivery is at-least-once: offsets are committed only after a message was handled, and BlockEdges is idempotent by hazard id,
so a redelivered event changes nothing. Messages that can never succeed (malformed, refused as invalid) are skipped, not retried forever.
"""
from __future__ import annotations

import json
import logging
import re
import threading
import time
from dataclasses import dataclass
from typing import Callable, Protocol

import grpc
from aegismesh.v1 import common_pb2

log = logging.getLogger("routing.consumer")
TOPIC = "incidents.verified.events"
HAZARD_EVENT = "HazardPolygonAdded"
_UUID = re.compile(r"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$")


class TransientError(Exception):
    """The routing service could not be reached or failed; the same message should be tried again."""


class PermanentError(Exception):
    """The routing service refused the message as invalid; retrying cannot help."""


@dataclass
class Message:
    topic: str
    key: str
    value: bytes
    partition: int = 0
    offset: int = 0


class Consumer(Protocol):
    def poll(self, timeout_s: float) -> list[Message]: ...
    def commit(self, messages: list[Message]) -> None: ...
    def close(self) -> None: ...


class FakeConsumer:
    """In-memory consumer for tests and local runs: hands out the queued messages and remembers what was committed."""

    def __init__(self, messages: list[Message] | None = None, batch: int = 100):
        self.queue, self.batch, self.committed, self.closed = list(messages or []), batch, [], False

    def feed(self, *messages: Message) -> None:
        self.queue.extend(messages)

    def poll(self, timeout_s: float) -> list[Message]:
        out, self.queue = self.queue[: self.batch], self.queue[self.batch:]
        return out

    def commit(self, messages: list[Message]) -> None:
        self.committed.extend(messages)

    def close(self) -> None:
        self.closed = True


class KafkaConsumer:
    """Redpanda/Kafka consumer (confluent-kafka). The package is imported when this is created, so tests and fakes do not need it."""

    def __init__(self, brokers: str, group: str = "routing-hazards", topic: str = TOPIC):
        try:
            from confluent_kafka import Consumer as _Consumer
        except ImportError as e:
            raise RuntimeError("confluent-kafka is required for KafkaConsumer: pip install confluent-kafka") from e
        self._c = _Consumer({"bootstrap.servers": brokers, "group.id": group, "enable.auto.commit": False, "auto.offset.reset": "earliest"})
        self._c.subscribe([topic])

    def poll(self, timeout_s: float) -> list[Message]:
        out = []
        for m in self._c.consume(num_messages=100, timeout=timeout_s):
            if m.error():
                log.warning("kafka error: %s", m.error())
                continue
            out.append(Message(m.topic(), (m.key() or b"").decode("utf-8", "replace"), m.value() or b"", m.partition(), m.offset()))
        return out

    def commit(self, messages: list[Message]) -> None:
        from confluent_kafka import TopicPartition
        last: dict[tuple[str, int], int] = {}
        for m in messages:
            k = (m.topic, m.partition)
            last[k] = max(last.get(k, -1), m.offset)
        if last:
            self._c.commit(offsets=[TopicPartition(t, p, o + 1) for (t, p), o in last.items()], asynchronous=False)

    def close(self) -> None:
        self._c.close()


class GrpcBlocker:
    """Calls BlockEdges on the routing service for one tenant and sorts failures into permanent and transient."""

    def __init__(self, stub):
        self.stub = stub

    def block(self, tenant: str, hazard: dict) -> dict:
        req = common_pb2.HazardPolygon(tenant_id=tenant, payload=json.dumps(hazard).encode())
        try:
            return json.loads(self.stub.BlockEdges(req, metadata=(("x-tenant-id", tenant),), timeout=30).payload)
        except grpc.RpcError as e:
            code = e.code()
            if code == grpc.StatusCode.INVALID_ARGUMENT:
                raise PermanentError(e.details()) from e
            if code == grpc.StatusCode.FAILED_PRECONDITION:   # the hazard is stored; only the re-plan of the last request failed
                log.warning("hazard %s stored, re-plan failed: %s", hazard.get("id"), e.details())
                return {"hazard_id": hazard.get("id"), "replanned": False}
            raise TransientError(f"{code.name}: {e.details()}") from e


class HazardConsumer:
    def __init__(self, consumer: Consumer, blocker, sleep: Callable[[float], None] = time.sleep, attempts: int = 5, backoff_s: float = 0.2):
        self.consumer, self.blocker, self.sleep, self.attempts, self.backoff_s = consumer, blocker, sleep, attempts, backoff_s
        self.stats = {"applied": 0, "ignored": 0, "skipped": 0}

    def _parse(self, msg: Message) -> tuple[str, dict] | None:
        """(tenant, hazard) for a hazard event, None for any other event; raises ValueError for a message that is not a valid event."""
        try:
            ev = json.loads(msg.value)
        except ValueError:
            raise ValueError("value is not JSON") from None
        if not isinstance(ev, dict) or not isinstance(ev.get("event_type"), str):
            raise ValueError("value is not an event")
        if ev["event_type"] != HAZARD_EVENT:
            return None
        tenant, payload = ev.get("tenant_id"), ev.get("payload")
        if not isinstance(tenant, str) or not _UUID.match(tenant):
            raise ValueError("tenant_id is missing or not a uuid")
        if not isinstance(payload, dict) or not payload.get("id") or "polygon" not in payload:
            raise ValueError("hazard payload needs id and polygon")
        return tenant, {k: payload[k] for k in ("id", "kind", "polygon") if k in payload}

    def handle(self, msg: Message) -> str:
        """Handles one message: 'applied', 'ignored' (another event type) or 'skipped' (cannot ever succeed). Raises TransientError when retries run out."""
        try:
            parsed = self._parse(msg)
        except ValueError as e:
            log.warning("skipping %s[%d]@%d: %s", msg.topic, msg.partition, msg.offset, e)
            self.stats["skipped"] += 1
            return "skipped"
        if parsed is None:
            self.stats["ignored"] += 1
            return "ignored"
        tenant, hazard = parsed
        for attempt in range(self.attempts):
            try:
                self.blocker.block(tenant, hazard)
                self.stats["applied"] += 1
                return "applied"
            except PermanentError as e:
                log.warning("skipping hazard %s for %s: refused as invalid: %s", hazard.get("id"), tenant, e)
                self.stats["skipped"] += 1
                return "skipped"
            except TransientError:
                if attempt == self.attempts - 1:
                    raise
                self.sleep(self.backoff_s * 2 ** attempt)
        raise AssertionError("unreachable")

    def run_once(self, timeout_s: float = 1.0) -> int:
        """Polls once and handles the batch in order; offsets of handled messages are committed even if a later one fails."""
        batch = self.consumer.poll(timeout_s)
        done: list[Message] = []
        try:
            for msg in batch:
                self.handle(msg)
                done.append(msg)
        finally:
            if done:
                self.consumer.commit(done)
        return len(done)

    def run(self, stop: threading.Event, timeout_s: float = 1.0) -> None:
        while not stop.is_set():
            self.run_once(timeout_s)
        self.consumer.close()
