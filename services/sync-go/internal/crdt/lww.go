package crdt

import "sort"

// Register is a last-writer-wins value: the higher Stamp wins on every replica.
type Register struct {
	Value   string `json:"value"`
	Stamp   Stamp  `json:"stamp"`
	Deleted bool   `json:"deleted,omitempty"`
}

func (r Register) wins(over Register) bool { return over.Stamp.Less(r.Stamp) }

// Map is a set of keyed registers; Delete writes a tombstone so it also wins or loses by stamp.
type Map struct{ Regs map[string]Register }

func NewMap() *Map { return &Map{Regs: map[string]Register{}} }

func (m *Map) Set(key, value string, at Stamp) map[string]Register {
	return m.put(key, Register{Value: value, Stamp: at})
}

func (m *Map) Delete(key string, at Stamp) map[string]Register {
	return m.put(key, Register{Stamp: at, Deleted: true})
}

func (m *Map) put(key string, r Register) map[string]Register {
	if cur, ok := m.Regs[key]; !ok || r.wins(cur) {
		m.Regs[key] = r
	}
	return map[string]Register{key: m.Regs[key]} // the delta to send
}

// Merge applies a delta or a full state, in any order, any number of times.
func (m *Map) Merge(delta map[string]Register) {
	for k, r := range delta {
		m.put(k, r)
	}
}

func (m *Map) Get(key string) (string, bool) {
	r, ok := m.Regs[key]
	if !ok || r.Deleted {
		return "", false
	}
	return r.Value, true
}

func (m *Map) Keys() []string {
	var out []string
	for k, r := range m.Regs {
		if !r.Deleted {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
