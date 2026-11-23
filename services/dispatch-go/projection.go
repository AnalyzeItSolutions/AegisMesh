package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	hashchain "snippet/hashchain"
)

// The read side (CQRS): views rebuilt from the event log, never written to directly.
type IncidentView struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Severity   string   `json:"severity"`
	Lat        float64  `json:"lat"`
	Lon        float64  `json:"lon"`
	H3Cell     uint64   `json:"h3_cell"`
	ReportedAt int64    `json:"reported_at"`
	Status     string   `json:"status"` // open, dispatched
	UnitIDs    []string `json:"unit_ids"`
}

type UnitView struct {
	ID         string `json:"id"`
	Status     string `json:"status"` // available, dispatched
	IncidentID string `json:"incident_id"`
}

type HazardView struct {
	ID      string       `json:"id"`
	Kind    string       `json:"kind"`
	Polygon [][2]float64 `json:"polygon"` // GeoJSON order: lon, lat
	Active  bool         `json:"active"`
}

type Projection struct {
	LastSeq      int64                    `json:"last_seq"`
	Incidents    map[string]*IncidentView `json:"incidents"`
	Units        map[string]*UnitView     `json:"units"`
	Hazards      map[string]*HazardView   `json:"hazards"`
	BlockedEdges map[string]bool          `json:"blocked_edges"`
}

func NewProjection() *Projection {
	return &Projection{Incidents: map[string]*IncidentView{}, Units: map[string]*UnitView{}, Hazards: map[string]*HazardView{}, BlockedEdges: map[string]bool{}}
}

func Rebuild(events []hashchain.Entry) (*Projection, error) {
	p := NewProjection()
	for _, e := range events {
		if err := p.Apply(e); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// Apply folds one event in. Replaying an event already applied is a no-op; skipping one is an error, so a view never silently misses history.
func (p *Projection) Apply(e hashchain.Entry) error {
	if e.SequenceID <= p.LastSeq {
		return nil
	}
	if e.SequenceID != p.LastSeq+1 {
		return fmt.Errorf("%w: have %d, got %d", ErrGap, p.LastSeq, e.SequenceID)
	}
	switch e.EventType {
	case IncidentReported:
		var v IncidentView
		if err := json.Unmarshal(e.Payload, &v); err != nil || v.ID == "" {
			return fmt.Errorf("%w: bad IncidentReported payload", ErrInvalid)
		}
		v.Status, v.UnitIDs = "open", []string{}
		p.Incidents[v.ID] = &v
	case UnitDispatched:
		var d struct {
			IncidentID string `json:"incident_id"`
			UnitID     string `json:"unit_id"`
		}
		if err := json.Unmarshal(e.Payload, &d); err != nil || d.IncidentID == "" || d.UnitID == "" {
			return fmt.Errorf("%w: bad UnitDispatched payload", ErrInvalid)
		}
		p.Units[d.UnitID] = &UnitView{ID: d.UnitID, Status: "dispatched", IncidentID: d.IncidentID}
		if inc := p.Incidents[d.IncidentID]; inc != nil {
			inc.Status = "dispatched"
			inc.UnitIDs = append(inc.UnitIDs, d.UnitID)
		}
	case RoadBlocked:
		var r struct {
			EdgeID string `json:"edge_id"`
		}
		if err := json.Unmarshal(e.Payload, &r); err != nil || r.EdgeID == "" {
			return fmt.Errorf("%w: bad RoadBlocked payload", ErrInvalid)
		}
		p.BlockedEdges[r.EdgeID] = true
	case HazardPolygonAdded:
		var h HazardView
		if err := json.Unmarshal(e.Payload, &h); err != nil || h.ID == "" || len(h.Polygon) < 3 {
			return fmt.Errorf("%w: bad HazardPolygonAdded payload", ErrInvalid)
		}
		h.Active = true
		p.Hazards[h.ID] = &h
	default:
		return fmt.Errorf("%w: %q", ErrUnknownType, e.EventType)
	}
	p.LastSeq = e.SequenceID
	return nil
}

func (p *Projection) clone() *Projection {
	b, _ := json.Marshal(p)
	c := NewProjection()
	_ = json.Unmarshal(b, c)
	return c
}

// Dispatcher is the write side: it checks a command against the view, appends the event and folds it in, one tenant at a time.
type Dispatcher struct {
	store    EventStore
	locks    *Locks
	lockTTL  time.Duration
	mu       sync.Mutex
	tenantMu map[string]*sync.Mutex
	views    map[string]*Projection
}

func NewDispatcher(store EventStore, locks *Locks) *Dispatcher {
	return &Dispatcher{store: store, locks: locks, lockTTL: 30 * time.Second, tenantMu: map[string]*sync.Mutex{}, views: map[string]*Projection{}}
}

func (d *Dispatcher) tenantLock(tenant string) *sync.Mutex {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.tenantMu[tenant] == nil {
		d.tenantMu[tenant] = &sync.Mutex{}
	}
	return d.tenantMu[tenant]
}

// view returns the tenant's projection, rebuilding it from the log the first time. The caller holds the tenant lock.
func (d *Dispatcher) view(ctx context.Context, tenant string) (*Projection, error) {
	if v := d.views[tenant]; v != nil {
		return v, nil
	}
	events, err := d.store.Load(ctx, tenant, 0, 0)
	if err != nil {
		return nil, err
	}
	v, err := Rebuild(events)
	if err != nil {
		return nil, err
	}
	d.views[tenant] = v
	return v, nil
}

// commit appends an event and folds it into the view; check runs first against the current view.
func (d *Dispatcher) commit(ctx context.Context, tenant, actor, aggregateID, eventType string, payload any, check func(*Projection) error) (hashchain.Entry, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return hashchain.Entry{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	tl := d.tenantLock(tenant)
	tl.Lock()
	defer tl.Unlock()
	v, err := d.view(ctx, tenant)
	if err != nil {
		return hashchain.Entry{}, err
	}
	if check != nil {
		if err := check(v); err != nil {
			return hashchain.Entry{}, err
		}
	}
	e, err := d.store.Append(ctx, tenant, aggregateID, eventType, raw, actor)
	if err != nil {
		return hashchain.Entry{}, err
	}
	if err := v.Apply(e); err != nil {
		delete(d.views, tenant) // the view is stale: rebuild it from the log on next use
		return e, err
	}
	return e, nil
}

func (d *Dispatcher) ReportIncident(ctx context.Context, tenant, actor string, in IncidentView) (hashchain.Entry, error) {
	if !uuidRe.MatchString(in.ID) || in.Kind == "" || in.Lat < -90 || in.Lat > 90 || in.Lon < -180 || in.Lon > 180 {
		return hashchain.Entry{}, fmt.Errorf("%w: incident needs a uuid id, a kind and a valid position", ErrInvalid)
	}
	switch in.Severity {
	case "black", "red", "yellow", "green":
	default:
		return hashchain.Entry{}, fmt.Errorf("%w: severity must be black, red, yellow or green", ErrInvalid)
	}
	return d.commit(ctx, tenant, actor, in.ID, IncidentReported, map[string]any{
		"id": in.ID, "kind": in.Kind, "severity": in.Severity, "lat": in.Lat, "lon": in.Lon, "h3_cell": in.H3Cell, "reported_at": in.ReportedAt,
	}, func(p *Projection) error {
		if p.Incidents[in.ID] != nil {
			return ErrExists
		}
		return nil
	})
}

// DispatchUnit sends a unit to an incident. The unit lock makes a concurrent second dispatch fail fast with ErrLocked, and the view
// check refuses a unit that is already out with ErrUnitBusy, so a unit is never dispatched twice.
func (d *Dispatcher) DispatchUnit(ctx context.Context, tenant, actor, incidentID, unitID string) (hashchain.Entry, error) {
	if !uuidRe.MatchString(incidentID) || !uuidRe.MatchString(unitID) {
		return hashchain.Entry{}, fmt.Errorf("%w: incident_id and unit_id must be uuids", ErrInvalid)
	}
	if err := d.locks.Acquire(tenant, unitID, actor, d.lockTTL); err != nil {
		return hashchain.Entry{}, err
	}
	defer d.locks.Release(tenant, unitID, actor) //nolint:errcheck
	return d.commit(ctx, tenant, actor, incidentID, UnitDispatched, map[string]string{"incident_id": incidentID, "unit_id": unitID}, func(p *Projection) error {
		if p.Incidents[incidentID] == nil {
			return fmt.Errorf("%w: incident %s", ErrNotFound, incidentID)
		}
		if u := p.Units[unitID]; u != nil && u.Status == "dispatched" {
			return ErrUnitBusy
		}
		return nil
	})
}

func (d *Dispatcher) AddHazard(ctx context.Context, tenant, actor string, h HazardView) (hashchain.Entry, error) {
	if !uuidRe.MatchString(h.ID) || len(h.Polygon) < 3 {
		return hashchain.Entry{}, fmt.Errorf("%w: hazard needs a uuid id and at least 3 polygon points", ErrInvalid)
	}
	return d.commit(ctx, tenant, actor, h.ID, HazardPolygonAdded, map[string]any{"id": h.ID, "kind": h.Kind, "polygon": h.Polygon}, func(p *Projection) error {
		if p.Hazards[h.ID] != nil {
			return ErrExists
		}
		return nil
	})
}

func (d *Dispatcher) BlockRoad(ctx context.Context, tenant, actor, aggregateID, edgeID string) (hashchain.Entry, error) {
	if edgeID == "" {
		return hashchain.Entry{}, fmt.Errorf("%w: edge_id is required", ErrInvalid)
	}
	return d.commit(ctx, tenant, actor, aggregateID, RoadBlocked, map[string]string{"edge_id": edgeID}, nil)
}

// Snapshot is a copy of the tenant's current view.
func (d *Dispatcher) Snapshot(ctx context.Context, tenant string) (*Projection, error) {
	tl := d.tenantLock(tenant)
	tl.Lock()
	defer tl.Unlock()
	v, err := d.view(ctx, tenant)
	if err != nil {
		return nil, err
	}
	return v.clone(), nil
}

// VerifyChain walks the tenant's whole chain.
func (d *Dispatcher) VerifyChain(ctx context.Context, tenant string) (VerifyResult, error) {
	return d.store.Verify(ctx, tenant)
}
