package main

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	h3 "github.com/uber/h3-go/v4"
)

// FrameRes is the H3 resolution every frame is indexed at (about 0.1 km2 per cell).
const FrameRes = 9

// MaxViewportCells bounds one subscription; a map viewport at resolution 7 is a few hundred cells.
const MaxViewportCells = 5000

// Frame is one unit position: the telemetry frame of the contract (gRPC payload, WebSocket frame and Redpanda value).
type Frame struct {
	UnitID  string  `json:"unit_id"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	H3Cell  uint64  `json:"h3_cell"`
	Heading float64 `json:"heading"`
	TS      int64   `json:"ts"`
}

// cellFor is the resolution-9 cell of a position.
func cellFor(lat, lon float64) (uint64, error) {
	c, err := h3.LatLngToCell(h3.NewLatLng(lat, lon), FrameRes)
	if err != nil {
		return 0, err
	}
	return uint64(c), nil
}

// checkCells validates a viewport: valid cells, no finer than a frame's cell (so a frame can be matched through its parent), not too many.
func checkCells(cells []uint64) error {
	if len(cells) > MaxViewportCells {
		return fmt.Errorf("a viewport has at most %d cells", MaxViewportCells)
	}
	for _, u := range cells {
		c := h3.Cell(u)
		if !c.IsValid() {
			return fmt.Errorf("%d is not a valid h3 cell", u)
		}
		if c.Resolution() > FrameRes {
			return fmt.Errorf("cell %d is finer than resolution %d", u, FrameRes)
		}
	}
	return nil
}

// Sub is one viewport subscriber. Frames arrive on C; a subscriber that cannot keep up loses its oldest frames, never the newest.
type Sub struct {
	C       chan Frame
	f       *Fanout
	tenant  string
	cells   map[int][]uint64 // resolution -> cells currently indexed
	dropped atomic.Int64
	closed  bool
}

// Dropped is how many frames this subscriber lost because it was slow.
func (s *Sub) Dropped() int64 { return s.dropped.Load() }

// Fanout routes frames to the viewport subscribers of their tenant. The index is tenant -> resolution -> cell -> subscribers, so a frame
// costs one parent lookup per distinct resolution in use plus a map hit, however many subscribers or cells there are.
type Fanout struct {
	mu     sync.RWMutex
	index  map[string]map[int]map[uint64]map[*Sub]struct{}
	lmu    sync.Mutex
	latest map[string]map[string]Frame
}

func NewFanout() *Fanout {
	return &Fanout{index: map[string]map[int]map[uint64]map[*Sub]struct{}{}, latest: map[string]map[string]Frame{}}
}

func (f *Fanout) Subscribe(tenant string, buffer int) *Sub {
	if buffer < 1 {
		buffer = 1
	}
	return &Sub{C: make(chan Frame, buffer), f: f, tenant: tenant, cells: map[int][]uint64{}}
}

// SetCells replaces the subscriber's viewport.
func (s *Sub) SetCells(cells []uint64) error {
	if err := checkCells(cells); err != nil {
		return err
	}
	f := s.f
	f.mu.Lock()
	defer f.mu.Unlock()
	if s.closed {
		return fmt.Errorf("subscription is closed")
	}
	s.unindex()
	byRes := f.index[s.tenant]
	if byRes == nil {
		byRes = map[int]map[uint64]map[*Sub]struct{}{}
		f.index[s.tenant] = byRes
	}
	for _, u := range cells {
		res := h3.Cell(u).Resolution()
		if byRes[res] == nil {
			byRes[res] = map[uint64]map[*Sub]struct{}{}
		}
		if byRes[res][u] == nil {
			byRes[res][u] = map[*Sub]struct{}{}
		}
		if _, dup := byRes[res][u][s]; !dup {
			byRes[res][u][s] = struct{}{}
			s.cells[res] = append(s.cells[res], u)
		}
	}
	return nil
}

// unindex removes the subscriber from the index; the caller holds f.mu for writing.
func (s *Sub) unindex() {
	byRes := s.f.index[s.tenant]
	for res, cells := range s.cells {
		for _, u := range cells {
			delete(byRes[res][u], s)
			if len(byRes[res][u]) == 0 {
				delete(byRes[res], u)
			}
		}
		if len(byRes[res]) == 0 {
			delete(byRes, res)
		}
	}
	s.cells = map[int][]uint64{}
	if len(byRes) == 0 {
		delete(s.f.index, s.tenant)
	}
}

// Close ends the subscription and closes C.
func (s *Sub) Close() {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.unindex()
	close(s.C)
}

// Publish records the unit's latest position and delivers the frame to every subscriber of the tenant whose viewport contains its cell.
func (f *Fanout) Publish(tenant string, fr Frame) {
	f.lmu.Lock()
	if f.latest[tenant] == nil {
		f.latest[tenant] = map[string]Frame{}
	}
	f.latest[tenant][fr.UnitID] = fr
	f.lmu.Unlock()

	cell := h3.Cell(fr.H3Cell)
	f.mu.RLock()
	defer f.mu.RUnlock()
	for res, cells := range f.index[tenant] {
		key := fr.H3Cell
		if res != FrameRes {
			p, err := cell.Parent(res)
			if err != nil {
				continue
			}
			key = uint64(p)
		}
		for s := range cells[key] {
			select {
			case s.C <- fr:
			default:
				select { // full: drop the oldest frame to make room for this one
				case <-s.C:
					s.dropped.Add(1)
				default:
				}
				select {
				case s.C <- fr:
				default:
					s.dropped.Add(1)
				}
			}
		}
	}
}

// Snapshot is the latest known position of every unit of the tenant inside the cells, in unit id order.
func (f *Fanout) Snapshot(tenant string, cells []uint64) []Frame {
	want := map[int]map[uint64]bool{}
	for _, u := range cells {
		res := h3.Cell(u).Resolution()
		if want[res] == nil {
			want[res] = map[uint64]bool{}
		}
		want[res][u] = true
	}
	f.lmu.Lock()
	defer f.lmu.Unlock()
	out := []Frame{}
	for _, fr := range f.latest[tenant] {
		for res, set := range want {
			key := fr.H3Cell
			if res != FrameRes {
				p, err := h3.Cell(fr.H3Cell).Parent(res)
				if err != nil {
					continue
				}
				key = uint64(p)
			}
			if set[key] {
				out = append(out, fr)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UnitID < out[j].UnitID })
	return out
}
