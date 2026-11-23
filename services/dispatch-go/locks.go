package main

import (
	"fmt"
	"sync"
	"time"
)

// Locks is the resource lock behind "a unit is never dispatched twice": one owner per (tenant, unit) at a time, with an expiry so a
// crashed dispatcher cannot hold a unit forever. It is per process; running several dispatch replicas needs the same interface on Redis.
type Locks struct {
	mu   sync.Mutex
	now  func() time.Time
	held map[string]lease
}

type lease struct {
	owner string
	until time.Time
}

func NewLocks() *Locks { return &Locks{now: time.Now, held: map[string]lease{}} }

func lockKey(tenant, unit string) string { return tenant + "/" + unit }

// Acquire takes the lock, or extends it for the same owner; another live owner gives ErrLocked.
func (l *Locks) Acquire(tenant, unit, owner string, ttl time.Duration) error {
	if tenant == "" || unit == "" || owner == "" || ttl <= 0 {
		return fmt.Errorf("%w: lock needs tenant, unit, owner and a positive ttl", ErrInvalid)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	k := lockKey(tenant, unit)
	if cur, ok := l.held[k]; ok && cur.owner != owner && l.now().Before(cur.until) {
		return ErrLocked
	}
	l.held[k] = lease{owner: owner, until: l.now().Add(ttl)}
	return nil
}

// Release frees the lock; only the owner may, and releasing a lock that expired and was taken by someone else is refused.
func (l *Locks) Release(tenant, unit, owner string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := lockKey(tenant, unit)
	cur, ok := l.held[k]
	if !ok {
		return nil
	}
	if cur.owner != owner && l.now().Before(cur.until) {
		return ErrLocked
	}
	if cur.owner == owner {
		delete(l.held, k)
	}
	return nil
}
