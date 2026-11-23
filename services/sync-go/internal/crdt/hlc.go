package crdt

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Stamp is a hybrid logical clock timestamp: ordered by wall time, then counter, then node id.
type Stamp struct {
	Wall    int64  `json:"wall"`
	Counter int64  `json:"counter"`
	Node    string `json:"node"`
}

func (a Stamp) Less(b Stamp) bool {
	if a.Wall != b.Wall {
		return a.Wall < b.Wall
	}
	if a.Counter != b.Counter {
		return a.Counter < b.Counter
	}
	return a.Node < b.Node
}

func (a Stamp) Encode() string { return fmt.Sprintf("%015d-%05d-%s", a.Wall, a.Counter, a.Node) }

func DecodeStamp(s string) (Stamp, error) {
	p := strings.SplitN(s, "-", 3)
	if len(p) != 3 {
		return Stamp{}, fmt.Errorf("bad stamp %q", s)
	}
	w, err1 := strconv.ParseInt(p[0], 10, 64)
	c, err2 := strconv.ParseInt(p[1], 10, 64)
	if err1 != nil || err2 != nil {
		return Stamp{}, fmt.Errorf("bad stamp %q", s)
	}
	return Stamp{w, c, p[2]}, nil
}

// HLC never goes backwards and orders local events after everything it has seen.
type HLC struct {
	mu      sync.Mutex
	node    string
	clock   func() int64
	wall    int64
	counter int64
}

func NewHLC(node string, clock func() int64) *HLC {
	if clock == nil {
		clock = func() int64 { return time.Now().UnixMilli() }
	}
	return &HLC{node: node, clock: clock}
}

func (h *HLC) Now() Stamp {
	h.mu.Lock()
	defer h.mu.Unlock()
	if phys := h.clock(); phys > h.wall {
		h.wall, h.counter = phys, 0
	} else {
		h.counter++
	}
	return Stamp{h.wall, h.counter, h.node}
}

func (h *HLC) Update(r Stamp) Stamp {
	h.mu.Lock()
	defer h.mu.Unlock()
	phys := h.clock()
	wall := max(phys, h.wall, r.Wall)
	switch {
	case wall == h.wall && wall == r.Wall:
		h.counter = max(h.counter, r.Counter) + 1
	case wall == h.wall:
		h.counter++
	case wall == r.Wall:
		h.counter = r.Counter + 1
	default:
		h.counter = 0
	}
	h.wall = wall
	return Stamp{h.wall, h.counter, h.node}
}
