"""Token bucket: `rate` tokens a second up to `capacity`; allow() spends tokens or says no. The clock is injectable so tests need no sleeping."""
from __future__ import annotations

import time


class TokenBucket:
    def __init__(self, rate: float, capacity: float, clock=time.monotonic):
        self.rate, self.capacity, self._clock = rate, capacity, clock
        self.tokens, self._last = float(capacity), clock()

    def allow(self, n: float = 1) -> bool:
        now = self._clock()
        self.tokens = min(self.capacity, self.tokens + (now - self._last) * self.rate)
        self._last = now
        if self.tokens >= n:
            self.tokens -= n
            return True
        return False


class KeyedLimiter:
    """One bucket per key (tenant, user, IP)."""
    def __init__(self, rate: float, capacity: float, clock=time.monotonic):
        self._args, self.buckets = (rate, capacity, clock), {}

    def allow(self, key: str, n: float = 1) -> bool:
        return self.buckets.setdefault(key, TokenBucket(*self._args)).allow(n)
