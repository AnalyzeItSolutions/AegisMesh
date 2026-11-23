package main

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	pb "github.com/aegismesh/aegismesh/gen/aegismeshv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	crdt "snippet/crdt"
)

// The tenant comes from the credentials the gateway verified (x-tenant-id), never from the payload.
const tenantHeader = "x-tenant-id"

type lwwEntry struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	HLC       int64  `json:"hlc"`
	ReplicaID string `json:"replica_id"`
	Deleted   bool   `json:"deleted,omitempty"`
}

type orAdd struct {
	Set     string `json:"set"`
	Element string `json:"element"`
	Tag     string `json:"tag"`
}

type orRemove struct {
	Set string `json:"set"`
	Tag string `json:"tag"`
}

// batch is the JSON inside DeltaBatch.payload (see CONTRACT.md).
type batch struct {
	ReplicaID string     `json:"replica_id"`
	Clock     int64      `json:"clock"`
	LWW       []lwwEntry `json:"lww"`
	Adds      []orAdd    `json:"orset_adds"`
	Removes   []orRemove `json:"orset_removes"`
}

type tenantState struct {
	regs *crdt.Map
	sets map[string]*crdt.ORSet
}

// Server keeps one CRDT state per tenant.
type Server struct {
	pb.UnimplementedSyncServiceServer
	node    string
	mu      sync.Mutex
	tenants map[string]*tenantState
}

func NewServer(node string) *Server {
	return &Server{node: node, tenants: map[string]*tenantState{}}
}

func tenantOf(ctx context.Context, fromRequest string) (string, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	vals := md.Get(tenantHeader)
	if len(vals) == 0 || vals[0] == "" {
		return "", status.Error(codes.Unauthenticated, "missing tenant credentials")
	}
	if fromRequest != "" && fromRequest != vals[0] {
		return "", status.Error(codes.InvalidArgument, "tenant_id does not match the credentials")
	}
	return vals[0], nil
}

// state must be called with s.mu held.
func (s *Server) state(tenant string) *tenantState {
	ts := s.tenants[tenant]
	if ts == nil {
		ts = &tenantState{regs: crdt.NewMap(), sets: map[string]*crdt.ORSet{}}
		s.tenants[tenant] = ts
	}
	return ts
}

func (ts *tenantState) set(name, node string) *crdt.ORSet {
	o := ts.sets[name]
	if o == nil {
		o = crdt.NewORSet(node)
		ts.sets[name] = o
	}
	return o
}

func stampOf(e lwwEntry) crdt.Stamp {
	return crdt.Stamp{Wall: e.HLC >> 16, Counter: e.HLC & 0xffff, Node: e.ReplicaID}
}

func hlcOf(st crdt.Stamp) int64 { return st.Wall<<16 | st.Counter }

func (b *batch) validate() error {
	for _, e := range b.LWW {
		if e.Key == "" || e.ReplicaID == "" || e.HLC < 0 {
			return status.Error(codes.InvalidArgument, "lww entries need key, replica_id and a non-negative hlc")
		}
	}
	for _, a := range b.Adds {
		if a.Set == "" || a.Element == "" || a.Tag == "" {
			return status.Error(codes.InvalidArgument, "orset_adds need set, element and tag")
		}
	}
	for _, r := range b.Removes {
		if r.Set == "" || r.Tag == "" {
			return status.Error(codes.InvalidArgument, "orset_removes need set and tag")
		}
	}
	return nil
}

type canonSet struct {
	Adds    map[string][]string `json:"adds"`
	Removed []string            `json:"removed"`
}

// hash is one SHA-256 over the registers and every non-empty set, with tags sorted so equal states hash equally.
func (ts *tenantState) hash() (string, error) {
	sets := map[string]canonSet{}
	for name, o := range ts.sets {
		d := o.State()
		c := canonSet{Adds: map[string][]string{}, Removed: append([]string{}, d.Removed...)}
		sort.Strings(c.Removed)
		for el, tags := range d.Adds {
			t := append([]string{}, tags...)
			sort.Strings(t)
			c.Adds[el] = t
		}
		if len(c.Adds) > 0 || len(c.Removed) > 0 {
			sets[name] = c
		}
	}
	return crdt.StateHash(ts.regs.Regs, sets)
}

func (s *Server) PushDelta(ctx context.Context, in *pb.DeltaBatch) (*pb.SyncAck, error) {
	tenant, err := tenantOf(ctx, in.GetTenantId())
	if err != nil {
		return nil, err
	}
	var b batch
	if err := json.Unmarshal(in.GetPayload(), &b); err != nil {
		return nil, status.Error(codes.InvalidArgument, "payload is not a valid delta batch")
	}
	if err := b.validate(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.state(tenant)
	for _, e := range b.LWW {
		ts.regs.Merge(map[string]crdt.Register{e.Key: {Value: e.Value, Stamp: stampOf(e), Deleted: e.Deleted}})
	}
	for _, a := range b.Adds {
		ts.set(a.Set, s.node).Merge(crdt.Delta{Adds: map[string][]string{a.Element: {a.Tag}}})
	}
	for _, r := range b.Removes {
		ts.set(r.Set, s.node).Merge(crdt.Delta{Removed: []string{r.Tag}})
	}
	h, err := ts.hash()
	if err != nil {
		return nil, status.Error(codes.Internal, "could not hash state")
	}
	out, _ := json.Marshal(map[string]any{"accepted": len(b.LWW) + len(b.Adds) + len(b.Removes), "hash": h})
	return &pb.SyncAck{TenantId: tenant, Payload: out}, nil
}

func (s *Server) PullDelta(ctx context.Context, in *pb.PullRequest) (*pb.DeltaBatch, error) {
	tenant, err := tenantOf(ctx, in.GetTenantId())
	if err != nil {
		return nil, err
	}
	var req struct {
		SinceHLC int64 `json:"since_hlc"`
	}
	if len(in.GetPayload()) > 0 {
		if err := json.Unmarshal(in.GetPayload(), &req); err != nil {
			return nil, status.Error(codes.InvalidArgument, "payload is not a valid pull request")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.state(tenant)
	b := batch{ReplicaID: s.node, LWW: []lwwEntry{}, Adds: []orAdd{}, Removes: []orRemove{}}
	keys := make([]string, 0, len(ts.regs.Regs))
	for k := range ts.regs.Regs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r := ts.regs.Regs[k]
		h := hlcOf(r.Stamp)
		if h > b.Clock {
			b.Clock = h
		}
		if h > req.SinceHLC {
			b.LWW = append(b.LWW, lwwEntry{Key: k, Value: r.Value, HLC: h, ReplicaID: r.Stamp.Node, Deleted: r.Deleted})
		}
	}
	names := make([]string, 0, len(ts.sets))
	for n := range ts.sets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		d := ts.sets[n].State()
		els := make([]string, 0, len(d.Adds))
		for el := range d.Adds {
			els = append(els, el)
		}
		sort.Strings(els)
		for _, el := range els {
			tags := append([]string{}, d.Adds[el]...)
			sort.Strings(tags)
			for _, t := range tags {
				b.Adds = append(b.Adds, orAdd{Set: n, Element: el, Tag: t})
			}
		}
		rem := append([]string{}, d.Removed...)
		sort.Strings(rem)
		for _, t := range rem {
			b.Removes = append(b.Removes, orRemove{Set: n, Tag: t})
		}
	}
	out, err := json.Marshal(b)
	if err != nil {
		return nil, status.Error(codes.Internal, "could not encode state")
	}
	return &pb.DeltaBatch{TenantId: tenant, Payload: out}, nil
}

func (s *Server) StateHash(ctx context.Context, in *pb.HashRequest) (*pb.HashReply, error) {
	tenant, err := tenantOf(ctx, in.GetTenantId())
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.state(tenant).hash()
	if err != nil {
		return nil, status.Error(codes.Internal, "could not hash state")
	}
	out, _ := json.Marshal(map[string]string{"hash": h})
	return &pb.HashReply{TenantId: tenant, Payload: out}, nil
}
