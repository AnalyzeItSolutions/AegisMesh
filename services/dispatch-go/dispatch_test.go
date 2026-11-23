package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	hashchain "snippet/hashchain"
)

const (
	tenantA = "11111111-1111-4111-8111-111111111111"
	tenantB = "22222222-2222-4222-8222-222222222222"
	inc1    = "aaaaaaaa-0000-4000-8000-000000000001"
	inc2    = "aaaaaaaa-0000-4000-8000-000000000002"
	unit1   = "bbbbbbbb-0000-4000-8000-000000000001"
	unit2   = "bbbbbbbb-0000-4000-8000-000000000002"
	actor   = "dispatcher-1"
)

var bg = context.Background()

func incident(id string) IncidentView {
	return IncidentView{ID: id, Kind: "flood", Severity: "red", Lat: 12.97, Lon: 77.59, H3Cell: 617700169958293503, ReportedAt: 1700000000000}
}

func TestChainsAreSeparatePerTenantAndVerify(t *testing.T) {
	s := NewMemoryStore()
	for i := 0; i < 5; i++ {
		if _, err := s.Append(bg, tenantA, inc1, IncidentReported, json.RawMessage(fmt.Sprintf(`{"n":%d}`, i)), actor); err != nil {
			t.Fatal(err)
		}
	}
	e, err := s.Append(bg, tenantB, inc2, IncidentReported, json.RawMessage(`{"n":0}`), actor)
	if err != nil || e.SequenceID != 1 || e.PrevHash != hashchain.Genesis {
		t.Fatalf("tenant B must start its own chain at 1: %+v %v", e, err)
	}
	for _, tn := range []string{tenantA, tenantB} {
		if r, _ := s.Verify(bg, tn); !r.OK || r.FirstBrokenSequenceID != nil {
			t.Fatalf("intact chain failed to verify: %+v", r)
		}
	}
	got, _ := s.Load(bg, tenantA, 3, 0)
	if len(got) != 2 || got[0].SequenceID != 4 {
		t.Fatalf("Load after 3 should give sequences 4 and 5, got %+v", got)
	}
	if got, _ := s.Load(bg, tenantA, 0, 2); len(got) != 2 {
		t.Fatalf("limit ignored: %d", len(got))
	}
}

func TestOneCharacterTamperIsNamed(t *testing.T) {
	s := NewMemoryStore()
	for i := 0; i < 10; i++ {
		s.Append(bg, tenantA, inc1, IncidentReported, json.RawMessage(`{"severity":"red"}`), actor) //nolint:errcheck
	}
	s.chains[tenantA][6].Payload = json.RawMessage(`{"severity":"rEd"}`)
	r, _ := s.Verify(bg, tenantA)
	if r.OK || r.FirstBrokenSequenceID == nil || *r.FirstBrokenSequenceID != 7 {
		t.Fatalf("expected the break at sequence 7, got %+v", r)
	}
}

func TestPayloadIsCanonicalisedSoJsonbCannotBreakTheChain(t *testing.T) {
	a, err := canonical(json.RawMessage(`{"b": 1, "a": [1, 2], "big": 617700169958293503}`)) // spacing and order as Postgres jsonb writes them
	if err != nil {
		t.Fatal(err)
	}
	b, _ := canonical(json.RawMessage(`{"big":617700169958293503,"a":[1,2],"b":1}`))
	if string(a) != string(b) || string(a) != `{"a":[1,2],"b":1,"big":617700169958293503}` {
		t.Fatalf("not canonical: %s vs %s", a, b)
	}
	if _, err := canonical(json.RawMessage(`{"a":1} {"b":2}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("trailing data must be refused: %v", err)
	}
}

func TestBadEventsAreRefusedAndChangeNothing(t *testing.T) {
	s := NewMemoryStore()
	cases := []struct {
		name                 string
		tenant, agg, typ, by string
		payload              string
		want                 error
	}{
		{"tenant not a uuid", "t1", inc1, IncidentReported, actor, `{}`, ErrInvalid},
		{"aggregate not a uuid", tenantA, "x", IncidentReported, actor, `{}`, ErrInvalid},
		{"unknown type", tenantA, inc1, "SomethingElse", actor, `{}`, ErrUnknownType},
		{"no actor", tenantA, inc1, IncidentReported, "", `{}`, ErrInvalid},
		{"payload not json", tenantA, inc1, IncidentReported, actor, `{oops`, ErrInvalid},
	}
	for _, c := range cases {
		if _, err := s.Append(bg, c.tenant, c.agg, c.typ, json.RawMessage(c.payload), c.by); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
	if got, _ := s.Load(bg, tenantA, 0, 0); len(got) != 0 {
		t.Fatalf("refused events reached the log: %+v", got)
	}
}

func TestProjectionRebuiltFromTheLogEqualsTheLiveView(t *testing.T) {
	st := NewMemoryStore()
	d := NewDispatcher(st, NewLocks())
	steps := []func() error{
		func() error { _, e := d.ReportIncident(bg, tenantA, actor, incident(inc1)); return e },
		func() error { _, e := d.ReportIncident(bg, tenantA, actor, incident(inc2)); return e },
		func() error { _, e := d.DispatchUnit(bg, tenantA, actor, inc1, unit1); return e },
		func() error {
			_, e := d.AddHazard(bg, tenantA, actor, HazardView{ID: "cccccccc-0000-4000-8000-000000000001", Kind: "flood", Polygon: [][2]float64{{77.5, 12.9}, {77.6, 12.9}, {77.6, 13.0}}})
			return e
		},
		func() error { _, e := d.BlockRoad(bg, tenantA, actor, inc1, "edge-42"); return e },
	}
	for i, s := range steps {
		if err := s(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	live, _ := d.Snapshot(bg, tenantA)
	events, _ := st.Load(bg, tenantA, 0, 0)
	rebuilt, err := Rebuild(events)
	if err != nil || !reflect.DeepEqual(live, rebuilt) {
		t.Fatalf("rebuilt view differs from the live view (%v):\n%+v\n%+v", err, live, rebuilt)
	}
	if live.Incidents[inc1].Status != "dispatched" || live.Incidents[inc2].Status != "open" || live.Units[unit1].Status != "dispatched" || !live.BlockedEdges["edge-42"] || len(live.Hazards) != 1 {
		t.Fatalf("view does not reflect the events: %+v", live)
	}
	if r, _ := d.VerifyChain(bg, tenantA); !r.OK || r.Checked != 5 {
		t.Fatalf("chain: %+v", r)
	}
	// a restarted dispatcher sees the same state from the log alone
	d2 := NewDispatcher(st, NewLocks())
	if again, _ := d2.Snapshot(bg, tenantA); !reflect.DeepEqual(again, live) {
		t.Fatal("a fresh dispatcher did not rebuild the same view")
	}
}

func TestProjectionRefusesGapsAndIgnoresReplays(t *testing.T) {
	p := NewProjection()
	e1 := hashchain.Append(nil, inc1, IncidentReported, json.RawMessage(`{"id":"`+inc1+`","kind":"fire"}`), actor)
	e2 := hashchain.Append([]hashchain.Entry{e1}, inc1, RoadBlocked, json.RawMessage(`{"edge_id":"e"}`), actor)
	e3 := hashchain.Append([]hashchain.Entry{e1, e2}, inc1, RoadBlocked, json.RawMessage(`{"edge_id":"f"}`), actor)
	if err := p.Apply(e1); err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(e3); !errors.Is(err, ErrGap) {
		t.Fatalf("skipping an event must be refused: %v", err)
	}
	if err := p.Apply(e1); err != nil || p.LastSeq != 1 {
		t.Fatalf("replaying an applied event must change nothing: %v %d", err, p.LastSeq)
	}
	bad := hashchain.Append([]hashchain.Entry{e1}, inc1, UnitDispatched, json.RawMessage(`{"incident_id":""}`), actor)
	if err := p.Apply(bad); !errors.Is(err, ErrInvalid) || p.LastSeq != 1 {
		t.Fatalf("a malformed event must be refused without moving the view: %v", err)
	}
}

func TestCommandChecks(t *testing.T) {
	d := NewDispatcher(NewMemoryStore(), NewLocks())
	if _, err := d.DispatchUnit(bg, tenantA, actor, inc1, unit1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("dispatching to an unknown incident: %v", err)
	}
	d.ReportIncident(bg, tenantA, actor, incident(inc1)) //nolint:errcheck
	if _, err := d.ReportIncident(bg, tenantA, actor, incident(inc1)); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate incident: %v", err)
	}
	bad := incident(inc2)
	bad.Severity = "purple"
	if _, err := d.ReportIncident(bg, tenantA, actor, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad severity: %v", err)
	}
	bad = incident(inc2)
	bad.Lat = 123
	if _, err := d.ReportIncident(bg, tenantA, actor, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad latitude: %v", err)
	}
	if _, err := d.AddHazard(bg, tenantA, actor, HazardView{ID: inc2, Polygon: [][2]float64{{0, 0}, {1, 1}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("two-point polygon: %v", err)
	}
	if r, _ := d.VerifyChain(bg, tenantA); r.Checked != 1 {
		t.Fatalf("only the one good command may be in the log: %+v", r)
	}
	// the same incident id in another tenant is a different incident
	if _, err := d.ReportIncident(bg, tenantB, actor, incident(inc1)); err != nil {
		t.Fatalf("tenant B: %v", err)
	}
}

func TestAUnitIsNeverDispatchedTwice(t *testing.T) {
	st := NewMemoryStore()
	d := NewDispatcher(st, NewLocks())
	d.ReportIncident(bg, tenantA, actor, incident(inc1)) //nolint:errcheck
	d.ReportIncident(bg, tenantA, actor, incident(inc2)) //nolint:errcheck
	var ok, busy, locked, other int32
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			inc := inc1
			if i%2 == 1 {
				inc = inc2
			}
			_, err := d.DispatchUnit(bg, tenantA, fmt.Sprintf("worker-%d", i), inc, unit1)
			switch {
			case err == nil:
				atomic.AddInt32(&ok, 1)
			case errors.Is(err, ErrUnitBusy):
				atomic.AddInt32(&busy, 1)
			case errors.Is(err, ErrLocked):
				atomic.AddInt32(&locked, 1)
			default:
				atomic.AddInt32(&other, 1)
			}
		}(i)
	}
	wg.Wait()
	events, _ := st.Load(bg, tenantA, 0, 0)
	n := 0
	for _, e := range events {
		if e.EventType == UnitDispatched {
			n++
		}
	}
	if ok != 1 || n != 1 || other != 0 || busy+locked != 39 {
		t.Fatalf("ok=%d dispatched events=%d busy=%d locked=%d other=%d", ok, n, busy, locked, other)
	}
	if _, err := d.DispatchUnit(bg, tenantA, actor, inc2, unit2); err != nil {
		t.Fatalf("a different unit must still be free: %v", err)
	}
	if r, _ := d.VerifyChain(bg, tenantA); !r.OK {
		t.Fatalf("chain broken by concurrent writers: %+v", r)
	}
}

func TestLocksExpireAndOnlyTheOwnerReleases(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLocks()
	l.now = func() time.Time { return now }
	if err := l.Acquire(tenantA, unit1, "a", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := l.Acquire(tenantA, unit1, "b", time.Minute); !errors.Is(err, ErrLocked) {
		t.Fatalf("second owner: %v", err)
	}
	if err := l.Acquire(tenantA, unit1, "a", time.Minute); err != nil {
		t.Fatalf("the owner may extend its lock: %v", err)
	}
	if err := l.Acquire(tenantB, unit1, "b", time.Minute); err != nil {
		t.Fatalf("the same unit id in another tenant is a different lock: %v", err)
	}
	if err := l.Release(tenantA, unit1, "b"); !errors.Is(err, ErrLocked) {
		t.Fatalf("a non-owner must not release: %v", err)
	}
	now = now.Add(2 * time.Minute)
	if err := l.Acquire(tenantA, unit1, "b", time.Minute); err != nil {
		t.Fatalf("an expired lock must be takeable: %v", err)
	}
	if err := l.Release(tenantA, unit1, "a"); !errors.Is(err, ErrLocked) {
		t.Fatalf("the old owner must not release the new owner's lock: %v", err)
	}
	if err := l.Acquire(tenantA, unit1, "c", time.Minute); !errors.Is(err, ErrLocked) {
		t.Fatalf("the new owner's lock must still hold: %v", err)
	}
	if err := l.Release(tenantA, unit1, "b"); err != nil || l.Acquire(tenantA, unit1, "c", time.Minute) != nil {
		t.Fatal("after the owner releases, the unit is free")
	}
	if err := l.Acquire(tenantA, unit1, "x", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero ttl: %v", err)
	}
}

// Runs against a real Postgres when DATABASE_URL is set (the schema is applied if the table is missing); skipped otherwise.
func TestPostgresStoreMatchesTheMemoryStore(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set: Postgres store not exercised")
	}
	ps, err := NewPGStore(bg, url)
	if err != nil {
		t.Fatal(err)
	}
	defer ps.Close()
	var reg *string
	if err := ps.pool.QueryRow(bg, `select to_regclass('event_store')::text`).Scan(&reg); err != nil {
		t.Fatal(err)
	}
	if reg == nil {
		sql, err := os.ReadFile("../../db/migrations/0006_event_store.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ps.pool.Exec(bg, string(sql)); err != nil {
			t.Fatalf("migration: %v", err)
		}
	}
	tenant := fmt.Sprintf("%08x-0000-4000-8000-%012x", time.Now().Unix()&0xffffffff, time.Now().UnixNano()&0xffffffffffff) // fresh per run: the table is append-only
	for i := 0; i < 5; i++ {
		e, err := ps.Append(bg, tenant, inc1, IncidentReported, json.RawMessage(fmt.Sprintf(`{"z": %d, "a": {"y": 1, "x": [1, 2]}}`, i)), actor)
		if err != nil || e.SequenceID != int64(i+1) {
			t.Fatalf("append %d: %+v %v", i, e, err)
		}
	}
	if r, err := ps.Verify(bg, tenant); err != nil || !r.OK || r.Checked != 5 {
		t.Fatalf("stored chain must re-verify after jsonb: %+v %v", r, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ps.Append(bg, tenant, inc2, IncidentReported, json.RawMessage(`{"c":1}`), actor); err != nil {
				t.Errorf("concurrent append: %v", err)
			}
		}()
	}
	wg.Wait()
	if r, _ := ps.Verify(bg, tenant); !r.OK || r.Checked != 13 {
		t.Fatalf("concurrent appends forked the chain: %+v", r)
	}
	_, err = ps.pool.Exec(bg, `update event_store set actor_id = 'x' where tenant_id = $1`, tenant)
	var pe interface{ Error() string }
	if err == nil {
		t.Fatal("event_store must refuse updates")
	}
	_ = pe
	if _, err := ps.Load(bg, "not-a-uuid", 0, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad tenant: %v", err)
	}
	_ = pgx.ErrNoRows
}
