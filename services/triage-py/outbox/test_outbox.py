import sqlite3

import outbox


def db():
    c = sqlite3.connect(":memory:"); outbox.init(c); return c


def test_publish_in_order_and_mark_sent():
    c, got = db(), []
    for i in range(3):
        outbox.enqueue(c, "t", {"i": i}, key=f"k{i}")
    assert outbox.drain(c, lambda t, k, p: got.append((t, k, p["i"]))) == {"sent": 3, "failed": 0}
    assert got == [("t", "k0", 0), ("t", "k1", 1), ("t", "k2", 2)] and outbox.pending(c) == []


def test_a_failed_publish_is_kept_and_retried_without_reordering():
    c, got, down = db(), [], [True]
    outbox.enqueue(c, "t", {"i": 0}); outbox.enqueue(c, "t", {"i": 1})

    def pub(t, k, p):
        if down[0]:
            raise ConnectionError("broker down")
        got.append(p["i"])
    assert outbox.drain(c, pub) == {"sent": 0, "failed": 1} and got == []
    assert [m["attempts"] for m in outbox.pending(c)] == [1, 0]
    down[0] = False
    assert outbox.drain(c, pub)["sent"] == 2 and got == [0, 1]


def test_rollback_drops_the_message_with_the_change():
    c = db()
    c.execute("create table thing (id integer)")
    c.execute("insert into thing values (1)"); outbox.enqueue(c, "t", {"id": 1}, commit=False); c.rollback()
    assert outbox.pending(c) == []
