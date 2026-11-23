from token_bucket import KeyedLimiter, TokenBucket


def test_burst_then_refill():
    t = [0.0]
    b = TokenBucket(rate=2, capacity=4, clock=lambda: t[0])
    assert [b.allow() for _ in range(5)] == [True, True, True, True, False]
    t[0] = 1.0
    assert b.allow() and b.allow() and not b.allow()
    t[0] = 100.0
    assert sum(b.allow() for _ in range(10)) == 4          # never more than capacity


def test_keys_are_independent():
    t = [0.0]
    k = KeyedLimiter(1, 1, clock=lambda: t[0])
    assert k.allow("a") and not k.allow("a") and k.allow("b")
