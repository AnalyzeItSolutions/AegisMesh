package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	hashchain "snippet/hashchain"
)

var (
	ErrInvalid     = errors.New("invalid argument")
	ErrNotFound    = errors.New("not found")
	ErrExists      = errors.New("already exists")
	ErrUnitBusy    = errors.New("unit is already dispatched")
	ErrLocked      = errors.New("unit is locked by another dispatch")
	ErrGap         = errors.New("event sequence gap")
	ErrUnknownType = errors.New("unknown event type")
)

// The four event types of the system; anything else is refused before it reaches the log.
const (
	IncidentReported   = "IncidentReported"
	UnitDispatched     = "UnitDispatched"
	RoadBlocked        = "RoadBlocked"
	HazardPolygonAdded = "HazardPolygonAdded"
)

var eventTypes = map[string]bool{IncidentReported: true, UnitDispatched: true, RoadBlocked: true, HazardPolygonAdded: true}
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// VerifyResult is what VerifyChain reports: the first broken sequence number, or none.
type VerifyResult struct {
	OK                    bool   `json:"ok"`
	FirstBrokenSequenceID *int64 `json:"first_broken_sequence_id"`
	Checked               int    `json:"checked"`
}

// EventStore is an append-only log with one hash chain per tenant (sequence numbers start at 1 for every tenant).
type EventStore interface {
	Append(ctx context.Context, tenant, aggregateID, eventType string, payload json.RawMessage, actorID string) (hashchain.Entry, error)
	// Load returns the tenant's events with sequence_id > after, in order; limit <= 0 means all.
	Load(ctx context.Context, tenant string, after int64, limit int) ([]hashchain.Entry, error)
	Verify(ctx context.Context, tenant string) (VerifyResult, error)
}

// canonical makes equal JSON byte-equal (sorted keys, no spaces, numbers untouched). Postgres jsonb rewrites spacing and key order,
// so every payload is canonicalised before it is hashed and again when it is read back; otherwise a stored event would never re-verify.
func canonical(raw json.RawMessage) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%w: payload is not JSON", ErrInvalid)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: payload has trailing data", ErrInvalid)
	}
	out, err := json.Marshal(v)
	return out, err
}

func validate(tenant, aggregateID, eventType string, payload json.RawMessage, actorID string) (json.RawMessage, error) {
	switch {
	case !uuidRe.MatchString(tenant):
		return nil, fmt.Errorf("%w: tenant must be a uuid", ErrInvalid)
	case !uuidRe.MatchString(aggregateID):
		return nil, fmt.Errorf("%w: aggregate_id must be a uuid", ErrInvalid)
	case !eventTypes[eventType]:
		return nil, fmt.Errorf("%w: %q", ErrUnknownType, eventType)
	case actorID == "":
		return nil, fmt.Errorf("%w: actor_id is required", ErrInvalid)
	}
	return canonical(payload)
}

func verifyChain(chain []hashchain.Entry) VerifyResult {
	i := hashchain.FirstBreak(chain)
	if i < 0 {
		return VerifyResult{OK: true, Checked: len(chain)}
	}
	seq := chain[i].SequenceID
	return VerifyResult{FirstBrokenSequenceID: &seq, Checked: i + 1}
}

// MemoryStore keeps the chains in memory: for tests, local runs and as the reference behaviour of the Postgres store.
type MemoryStore struct {
	mu     sync.Mutex
	chains map[string][]hashchain.Entry
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{chains: map[string][]hashchain.Entry{}} }

func (m *MemoryStore) Append(_ context.Context, tenant, aggregateID, eventType string, payload json.RawMessage, actorID string) (hashchain.Entry, error) {
	p, err := validate(tenant, aggregateID, eventType, payload, actorID)
	if err != nil {
		return hashchain.Entry{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := hashchain.Append(m.chains[tenant], aggregateID, eventType, p, actorID)
	m.chains[tenant] = append(m.chains[tenant], e)
	return e, nil
}

func (m *MemoryStore) Load(_ context.Context, tenant string, after int64, limit int) ([]hashchain.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []hashchain.Entry
	for _, e := range m.chains[tenant] {
		if e.SequenceID > after {
			out = append(out, e)
			if limit > 0 && len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (m *MemoryStore) Verify(ctx context.Context, tenant string) (VerifyResult, error) {
	all, _ := m.Load(ctx, tenant, 0, 0)
	return verifyChain(all), nil
}

// PGStore keeps the chains in the event_store table (db/migrations/0006_event_store.sql). Every call runs in a transaction that sets
// app.tenant_id, so row-level security applies, and appends take a per-tenant advisory lock so two writers cannot fork the chain.
type PGStore struct{ pool *pgxpool.Pool }

func NewPGStore(ctx context.Context, url string) (*PGStore, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	return &PGStore{pool: pool}, nil
}

func (s *PGStore) Close() { s.pool.Close() }

func (s *PGStore) inTenantTx(ctx context.Context, tenant string, fn func(pgx.Tx) error) error {
	if !uuidRe.MatchString(tenant) {
		return fmt.Errorf("%w: tenant must be a uuid", ErrInvalid)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `select set_config('app.tenant_id', $1, true)`, tenant); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PGStore) Append(ctx context.Context, tenant, aggregateID, eventType string, payload json.RawMessage, actorID string) (hashchain.Entry, error) {
	p, err := validate(tenant, aggregateID, eventType, payload, actorID)
	if err != nil {
		return hashchain.Entry{}, err
	}
	var e hashchain.Entry
	err = s.inTenantTx(ctx, tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, tenant); err != nil {
			return err
		}
		var chain []hashchain.Entry
		var last hashchain.Entry
		err := tx.QueryRow(ctx, `select sequence_id, entry_hash from event_store where tenant_id = $1 order by sequence_id desc limit 1`, tenant).Scan(&last.SequenceID, &last.EntryHash)
		if err == nil {
			chain = []hashchain.Entry{last} // Append only reads the last entry of the chain
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		e = hashchain.Append(chain, aggregateID, eventType, p, actorID)
		_, err = tx.Exec(ctx, `insert into event_store (sequence_id, tenant_id, aggregate_id, event_type, payload, actor_id, prev_hash, entry_hash)
			values ($1, $2, $3, $4, $5::jsonb, $6, $7, $8)`, e.SequenceID, tenant, aggregateID, eventType, string(p), actorID, e.PrevHash, e.EntryHash)
		return err
	})
	return e, err
}

func (s *PGStore) Load(ctx context.Context, tenant string, after int64, limit int) ([]hashchain.Entry, error) {
	var out []hashchain.Entry
	err := s.inTenantTx(ctx, tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select sequence_id, aggregate_id::text, event_type, payload::text, actor_id, prev_hash, entry_hash
			from event_store where tenant_id = $1 and sequence_id > $2 order by sequence_id limit case when $3 > 0 then $3 else null end`, tenant, after, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e hashchain.Entry
			var raw string
			if err := rows.Scan(&e.SequenceID, &e.AggregateID, &e.EventType, &raw, &e.ActorID, &e.PrevHash, &e.EntryHash); err != nil {
				return err
			}
			if e.Payload, err = canonical(json.RawMessage(raw)); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

func (s *PGStore) Verify(ctx context.Context, tenant string) (VerifyResult, error) {
	all, err := s.Load(ctx, tenant, 0, 0)
	if err != nil {
		return VerifyResult{}, err
	}
	return verifyChain(all), nil
}

// PGCursors keeps the outbox cursors in the outbox_cursor table (db/migrations/0007_outbox_cursor.sql), under the same row-level security.
type PGCursors struct{ s *PGStore }

func (s *PGStore) Cursors() Cursors { return PGCursors{s} }

func (c PGCursors) Get(ctx context.Context, tenant string) (int64, error) {
	var seq int64
	err := c.s.inTenantTx(ctx, tenant, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select last_sequence_id from outbox_cursor where tenant_id = $1`, tenant).Scan(&seq)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return seq, err
}

// Set never moves a cursor backwards.
func (c PGCursors) Set(ctx context.Context, tenant string, seq int64) error {
	return c.s.inTenantTx(ctx, tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into outbox_cursor (tenant_id, last_sequence_id) values ($1, $2)
			on conflict (tenant_id) do update set last_sequence_id = greatest(outbox_cursor.last_sequence_id, excluded.last_sequence_id), updated_at = now()`, tenant, seq)
		return err
	})
}

// StoreFromEnv is the Postgres store when DATABASE_URL is set, otherwise the in-memory one.
func StoreFromEnv(ctx context.Context) (EventStore, error) {
	if url := os.Getenv("DATABASE_URL"); url != "" {
		return NewPGStore(ctx, url)
	}
	return NewMemoryStore(), nil
}
