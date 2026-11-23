package crdt

import (
	"fmt"
	"sort"
)

// Delta is what an ORSet change sends: tags added per element and tags removed.
type Delta struct {
	Adds    map[string][]string `json:"adds"`
	Removed []string            `json:"removed"`
}

// ORSet is an observed-remove set where a concurrent add wins over a remove.
type ORSet struct {
	node    string
	n       int
	adds    map[string]map[string]bool
	removed map[string]bool
}

func NewORSet(node string) *ORSet {
	return &ORSet{node: node, adds: map[string]map[string]bool{}, removed: map[string]bool{}}
}

func (s *ORSet) Add(elem string) Delta {
	s.n++
	tag := fmt.Sprintf("%s:%d", s.node, s.n)
	if s.adds[elem] == nil {
		s.adds[elem] = map[string]bool{}
	}
	s.adds[elem][tag] = true
	return Delta{Adds: map[string][]string{elem: {tag}}}
}

// Remove drops only the tags this replica has seen, so an add made elsewhere at the same time survives.
func (s *ORSet) Remove(elem string) Delta {
	var tags []string
	for t := range s.adds[elem] {
		if !s.removed[t] {
			tags = append(tags, t)
		}
	}
	sort.Strings(tags)
	for _, t := range tags {
		s.removed[t] = true
	}
	return Delta{Adds: map[string][]string{}, Removed: tags}
}

func (s *ORSet) Merge(d Delta) {
	for e, tags := range d.Adds {
		if s.adds[e] == nil {
			s.adds[e] = map[string]bool{}
		}
		for _, t := range tags {
			s.adds[e][t] = true
		}
	}
	for _, t := range d.Removed {
		s.removed[t] = true
	}
}

func (s *ORSet) Contains(elem string) bool {
	for t := range s.adds[elem] {
		if !s.removed[t] {
			return true
		}
	}
	return false
}

func (s *ORSet) Elements() []string {
	var out []string
	for e := range s.adds {
		if s.Contains(e) {
			out = append(out, e)
		}
	}
	sort.Strings(out)
	return out
}

// State is the full state in a canonical form (sorted), for hashing and for sending whole.
func (s *ORSet) State() Delta {
	d := Delta{Adds: map[string][]string{}}
	for e, tags := range s.adds {
		for t := range tags {
			d.Adds[e] = append(d.Adds[e], t)
		}
		sort.Strings(d.Adds[e])
	}
	for t := range s.removed {
		d.Removed = append(d.Removed, t)
	}
	sort.Strings(d.Removed)
	return d
}
