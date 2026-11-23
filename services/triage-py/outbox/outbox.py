"""Transactional outbox on SQLite (or any DB-API connection with the same SQL): write the message in the same transaction as the change, publish later,
mark it sent only after the publish succeeded. A crash between publish and mark re-sends it, so consumers must be idempotent (at-least-once)."""
from __future__ import annotations

import json
import time

SCHEMA = "create table if not exists outbox (id integer primary key autoincrement, topic text not null, key text, payload text not null, created real not null, sent real, attempts integer not null default 0, last_error text)"


def init(conn) -> None:
    conn.execute(SCHEMA); conn.commit()


def enqueue(conn, topic: str, payload: dict, key: str | None = None, commit: bool = True) -> int:
    cur = conn.execute("insert into outbox (topic, key, payload, created) values (?, ?, ?, ?)", (topic, key, json.dumps(payload, sort_keys=True), time.time()))
    if commit:
        conn.commit()
    return cur.lastrowid


def pending(conn, limit: int = 100) -> list[dict]:
    rows = conn.execute("select id, topic, key, payload, attempts from outbox where sent is null order by id limit ?", (limit,)).fetchall()
    return [{"id": r[0], "topic": r[1], "key": r[2], "payload": json.loads(r[3]), "attempts": r[4]} for r in rows]


def drain(conn, publish, limit: int = 100) -> dict:
    """publish(topic, key, payload) raises on failure. Messages are sent in id order; the first failure stops the batch so order is kept."""
    sent = failed = 0
    for m in pending(conn, limit):
        try:
            publish(m["topic"], m["key"], m["payload"])
        except Exception as e:  # noqa: BLE001
            conn.execute("update outbox set attempts = attempts + 1, last_error = ? where id = ?", (str(e)[:300], m["id"])); conn.commit()
            failed += 1
            break
        conn.execute("update outbox set sent = ? where id = ?", (time.time(), m["id"])); conn.commit()
        sent += 1
    return {"sent": sent, "failed": failed}
