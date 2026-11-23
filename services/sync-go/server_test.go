package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	pb "github.com/aegismesh/aegismesh/gen/aegismeshv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func ctxFor(tenant string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(tenantHeader, tenant))
}

func push(t *testing.T, s *Server, tenant string, b batch) {
	t.Helper()
	p, _ := json.Marshal(b)
	if _, err := s.PushDelta(ctxFor(tenant), &pb.DeltaBatch{Payload: p}); err != nil {
		t.Fatalf("push: %v", err)
	}
}

func pull(t *testing.T, s *Server, tenant string) batch {
	t.Helper()
	out, err := s.PullDelta(ctxFor(tenant), &pb.PullRequest{})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	var b batch
	if err := json.Unmarshal(out.Payload, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func hashOf(t *testing.T, s *Server, tenant string) string {
	t.Helper()
	out, err := s.StateHash(ctxFor(tenant), &pb.HashRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(out.Payload, &m); err != nil || len(m["hash"]) != 64 {
		t.Fatalf("bad hash reply %s", out.Payload)
	}
	return m["hash"]
}

func value(b batch, key string) (string, bool) {
	for _, e := range b.LWW {
		if e.Key == key && !e.Deleted {
			return e.Value, true
		}
	}
	return "", false
}

func TestTwoReplicasConvergeToOneHash(t *testing.T) {
	a, b := NewServer("a"), NewServer("b")
	// conflicting writes to one key, and a concurrent remove/add of one set element
	push(t, a, "t1", batch{ReplicaID: "r1", LWW: []lwwEntry{{Key: "incident:1", Value: "old", HLC: 100 << 16, ReplicaID: "r1"}}, Adds: []orAdd{{Set: "units", Element: "u1", Tag: "r1:1"}}})
	push(t, b, "t1", batch{ReplicaID: "r2", LWW: []lwwEntry{{Key: "incident:1", Value: "new", HLC: 100<<16 | 1, ReplicaID: "r2"}},
		Adds: []orAdd{{Set: "units", Element: "u1", Tag: "r2:1"}}, Removes: []orRemove{{Set: "units", Tag: "r1:1"}}})
	if hashOf(t, a, "t1") == hashOf(t, b, "t1") {
		t.Fatal("diverged replicas must not share a hash yet")
	}
	// exchange in opposite orders, and twice: merges are idempotent
	pa, pb2 := pull(t, a, "t1"), pull(t, b, "t1")
	push(t, b, "t1", pa)
	push(t, a, "t1", pb2)
	push(t, a, "t1", pb2)
	if ha, hb := hashOf(t, a, "t1"), hashOf(t, b, "t1"); ha != hb {
		t.Fatalf("hashes differ after sync: %s vs %s", ha, hb)
	}
	if v, ok := value(pull(t, a, "t1"), "incident:1"); !ok || v != "new" {
		t.Fatalf("the higher stamp must win, got %q", v)
	}
	// the add made at the same time as the remove survives (add wins)
	got := pull(t, b, "t1")
	live := false
	for _, ad := range got.Adds {
		if ad.Element == "u1" && ad.Tag == "r2:1" {
			live = true
		}
	}
	removed := map[string]bool{}
	for _, r := range got.Removes {
		removed[r.Tag] = true
	}
	if !live || removed["r2:1"] || !removed["r1:1"] {
		t.Fatalf("add-wins violated: adds=%v removes=%v", got.Adds, got.Removes)
	}
}

func TestTombstoneBeatsOlderWriteInAnyOrder(t *testing.T) {
	del := batch{LWW: []lwwEntry{{Key: "k", HLC: 9 << 16, ReplicaID: "r2", Deleted: true}}}
	set := batch{LWW: []lwwEntry{{Key: "k", Value: "v", HLC: 5 << 16, ReplicaID: "r1"}}}
	x, y := NewServer("x"), NewServer("y")
	push(t, x, "t", set)
	push(t, x, "t", del)
	push(t, y, "t", del)
	push(t, y, "t", set)
	if hashOf(t, x, "t") != hashOf(t, y, "t") {
		t.Fatal("order of arrival changed the state")
	}
	if _, ok := value(pull(t, x, "t"), "k"); ok {
		t.Fatal("deleted key is still visible")
	}
}

func TestTenantsAreIsolated(t *testing.T) {
	s := NewServer("s")
	push(t, s, "t1", batch{LWW: []lwwEntry{{Key: "k", Value: "v", HLC: 1 << 16, ReplicaID: "r"}}})
	if hashOf(t, s, "t1") == hashOf(t, s, "t2") {
		t.Fatal("tenant t2 sees tenant t1 data")
	}
	if _, ok := value(pull(t, s, "t2"), "k"); ok {
		t.Fatal("tenant t2 pulled tenant t1 data")
	}
}

func TestRefusals(t *testing.T) {
	s := NewServer("s")
	code := func(err error) codes.Code { return status.Code(err) }
	if _, err := s.PushDelta(context.Background(), &pb.DeltaBatch{Payload: []byte(`{}`)}); code(err) != codes.Unauthenticated {
		t.Fatalf("no credentials: %v", err)
	}
	if _, err := s.PushDelta(ctxFor("t1"), &pb.DeltaBatch{TenantId: "other", Payload: []byte(`{}`)}); code(err) != codes.InvalidArgument {
		t.Fatalf("tenant mismatch: %v", err)
	}
	if _, err := s.PushDelta(ctxFor("t1"), &pb.DeltaBatch{Payload: []byte(`not json`)}); code(err) != codes.InvalidArgument {
		t.Fatalf("bad json: %v", err)
	}
	bad := []byte(`{"lww":[{"key":"","value":"v","hlc":1,"replica_id":"r"}]}`)
	if _, err := s.PushDelta(ctxFor("t1"), &pb.DeltaBatch{Payload: bad}); code(err) != codes.InvalidArgument {
		t.Fatalf("empty key: %v", err)
	}
	if _, err := s.StateHash(context.Background(), &pb.HashRequest{}); code(err) != codes.Unauthenticated {
		t.Fatalf("hash without credentials: %v", err)
	}
}

func TestRefusedBatchChangesNothing(t *testing.T) {
	s := NewServer("s")
	before := hashOf(t, s, "t1")
	p := []byte(`{"lww":[{"key":"ok","value":"v","hlc":1,"replica_id":"r"},{"key":"","value":"x","hlc":1,"replica_id":"r"}]}`)
	if _, err := s.PushDelta(ctxFor("t1"), &pb.DeltaBatch{Payload: p}); err == nil {
		t.Fatal("expected a refusal")
	}
	if hashOf(t, s, "t1") != before {
		t.Fatal("a refused batch was partly applied")
	}
}

func pullSince(t *testing.T, s *Server, tenant string, since int64) batch {
	t.Helper()
	out, err := s.PullDelta(ctxFor(tenant), &pb.PullRequest{Payload: []byte(fmt.Sprintf(`{"since_seq":%d}`, since))})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	var b batch
	if err := json.Unmarshal(out.Payload, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAWriteThatArrivesLateWithAnOldClockIsStillDeliveredToThoseWhoPulledBefore(t *testing.T) {
	s := NewServer("s")
	push(t, s, "t1", batch{LWW: []lwwEntry{{Key: "new", Value: "n", HLC: 100 << 16, ReplicaID: "r1"}}})
	first := pullSince(t, s, "t1", 0)
	if len(first.LWW) != 1 || first.Cursor != 1 {
		t.Fatalf("first pull: %+v", first)
	}
	// a device that was offline pushes a write made long before: its clock is older than everything already pulled
	push(t, s, "t1", batch{LWW: []lwwEntry{{Key: "old", Value: "o", HLC: 5 << 16, ReplicaID: "r2"}}})
	next := pullSince(t, s, "t1", first.Cursor)
	if len(next.LWW) != 1 || next.LWW[0].Key != "old" || next.Cursor != 2 {
		t.Fatalf("the late write was not delivered: %+v", next)
	}
	if again := pullSince(t, s, "t1", next.Cursor); len(again.LWW) != 0 || again.Cursor != 2 {
		t.Fatalf("nothing is new: %+v", again)
	}
}

func TestOnlyARealChangeMovesTheCursor(t *testing.T) {
	s := NewServer("s")
	w := batch{LWW: []lwwEntry{{Key: "k", Value: "v", HLC: 9 << 16, ReplicaID: "r1"}}}
	push(t, s, "t1", w)
	push(t, s, "t1", w)                                                                                 // the same write again: no change
	push(t, s, "t1", batch{LWW: []lwwEntry{{Key: "k", Value: "stale", HLC: 1 << 16, ReplicaID: "r2"}}}) // an older write loses: no change
	if c := pullSince(t, s, "t1", 0).Cursor; c != 1 {
		t.Fatalf("cursor %d, want 1", c)
	}
	push(t, s, "t1", batch{LWW: []lwwEntry{{Key: "k", Value: "v2", HLC: 10 << 16, ReplicaID: "r2"}}})
	got := pullSince(t, s, "t1", 1)
	if got.Cursor != 2 || len(got.LWW) != 1 || got.LWW[0].Value != "v2" {
		t.Fatalf("a newer write must move the cursor: %+v", got)
	}
}
