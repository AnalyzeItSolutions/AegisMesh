package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	pb "github.com/aegismesh/aegismesh/gen/aegismeshv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type rec struct {
	topic, key string
	value      []byte
}

// fakePublisher records what it accepts and can be told to fail the next n calls.
type fakePublisher struct {
	mu      sync.Mutex
	failing int
	got     []rec
}

func (f *fakePublisher) Publish(_ context.Context, topic, key string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing > 0 {
		f.failing--
		return errors.New("broker unavailable")
	}
	f.got = append(f.got, rec{topic, key, value})
	return nil
}

func (f *fakePublisher) on(topic string) []rec {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []rec
	for _, r := range f.got {
		if r.topic == topic {
			out = append(out, r)
		}
	}
	return out
}

func newServer(pub Publisher) (*Server, *Outbox, *MemoryStore) {
	st := NewMemoryStore()
	ob := NewOutbox(st, pub, NewMemCursors())
	return NewServer(NewDispatcher(st, NewLocks()), ob), ob, st
}

func as(tenant, who string) context.Context {
	return metadata.NewIncomingContext(bg, metadata.Pairs(tenantHeader, tenant, actorHeader, who))
}

func j(v any) []byte { b, _ := json.Marshal(v); return b }

func incidentJSON(id string) []byte {
	return j(map[string]any{"id": id, "kind": "flood", "severity": "red", "lat": 12.97, "lon": 77.59, "h3_cell": 617700169958293503, "reported_at": 1700000000000})
}

func code(err error) codes.Code { return status.Code(err) }

func TestCommandsAppendAndTheChainVerifies(t *testing.T) {
	s, _, _ := newServer(&fakePublisher{})
	ctx := as(tenantA, "u1")
	ack, err := s.ReportIncident(ctx, &pb.IncidentReport{Payload: incidentJSON(inc1)})
	if err != nil {
		t.Fatal(err)
	}
	var a map[string]any
	json.Unmarshal(ack.Payload, &a) //nolint:errcheck
	if a["sequence_id"].(float64) != 1 || len(a["entry_hash"].(string)) != 64 {
		t.Fatalf("ack: %s", ack.Payload)
	}
	if _, err := s.DispatchUnit(ctx, &pb.DispatchRequest{Payload: j(map[string]string{"incident_id": inc1, "unit_id": unit1})}); err != nil {
		t.Fatal(err)
	}
	poly := [][2]float64{{77.5, 12.9}, {77.6, 12.9}, {77.6, 13.0}}
	if _, err := s.AddHazard(ctx, &pb.HazardPolygon{Payload: j(map[string]any{"id": "cccccccc-0000-4000-8000-000000000001", "kind": "flood", "polygon": poly})}); err != nil {
		t.Fatal(err)
	}
	rep, err := s.VerifyChain(ctx, &pb.VerifyRequest{})
	var v VerifyResult
	json.Unmarshal(rep.Payload, &v) //nolint:errcheck
	if err != nil || !v.OK || v.Checked != 3 {
		t.Fatalf("verify: %s %v", rep.Payload, err)
	}
}

func TestErrorsBecomeTheRightGrpcCodes(t *testing.T) {
	s, _, _ := newServer(&fakePublisher{})
	ctx := as(tenantA, "u1")
	s.ReportIncident(ctx, &pb.IncidentReport{Payload: incidentJSON(inc1)})                                         //nolint:errcheck
	s.DispatchUnit(ctx, &pb.DispatchRequest{Payload: j(map[string]string{"incident_id": inc1, "unit_id": unit1})}) //nolint:errcheck
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"no credentials", func() error { _, e := s.ReportIncident(bg, &pb.IncidentReport{Payload: incidentJSON(inc2)}); return e }(), codes.Unauthenticated},
		{"no actor", func() error {
			_, e := s.VerifyChain(metadata.NewIncomingContext(bg, metadata.Pairs(tenantHeader, tenantA)), &pb.VerifyRequest{})
			return e
		}(), codes.Unauthenticated},
		{"tenant mismatch", func() error { _, e := s.VerifyChain(ctx, &pb.VerifyRequest{TenantId: tenantB}); return e }(), codes.InvalidArgument},
		{"not json", func() error { _, e := s.ReportIncident(ctx, &pb.IncidentReport{Payload: []byte("nope")}); return e }(), codes.InvalidArgument},
		{"unknown field", func() error {
			_, e := s.DispatchUnit(ctx, &pb.DispatchRequest{Payload: j(map[string]string{"incident_id": inc1, "unit_id": unit2, "status": "x"})})
			return e
		}(), codes.InvalidArgument},
		{"bad severity", func() error {
			_, e := s.ReportIncident(ctx, &pb.IncidentReport{Payload: j(map[string]any{"id": inc2, "kind": "f", "severity": "purple", "lat": 1, "lon": 1})})
			return e
		}(), codes.InvalidArgument},
		{"duplicate incident", func() error { _, e := s.ReportIncident(ctx, &pb.IncidentReport{Payload: incidentJSON(inc1)}); return e }(), codes.AlreadyExists},
		{"unknown incident", func() error {
			_, e := s.DispatchUnit(ctx, &pb.DispatchRequest{Payload: j(map[string]string{"incident_id": inc2, "unit_id": unit2})})
			return e
		}(), codes.NotFound},
		{"unit already out", func() error {
			_, e := s.DispatchUnit(ctx, &pb.DispatchRequest{Payload: j(map[string]string{"incident_id": inc1, "unit_id": unit1})})
			return e
		}(), codes.FailedPrecondition},
	}
	for _, c := range cases {
		if code(c.err) != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.err, c.want)
		}
	}
	if code(toStatus(errors.New("secret database detail"))) != codes.Internal || toStatus(errors.New("secret database detail")).Error() == "secret database detail" {
		t.Error("unexpected errors must not leak their text")
	}
}

func TestTenantsDoNotSeeEachOthersChain(t *testing.T) {
	s, _, _ := newServer(&fakePublisher{})
	s.ReportIncident(as(tenantA, "u"), &pb.IncidentReport{Payload: incidentJSON(inc1)}) //nolint:errcheck
	rep, _ := s.VerifyChain(as(tenantB, "u"), &pb.VerifyRequest{})
	var v VerifyResult
	json.Unmarshal(rep.Payload, &v) //nolint:errcheck
	if v.Checked != 0 {
		t.Fatalf("tenant B sees %d of tenant A's events", v.Checked)
	}
}

func TestOutboxPublishesEveryEventInOrderOnceDespiteBrokerFailures(t *testing.T) {
	pub := &fakePublisher{}
	s, ob, _ := newServer(pub)
	ctx := as(tenantA, "u1")
	s.ReportIncident(ctx, &pb.IncidentReport{Payload: incidentJSON(inc1)})                                         //nolint:errcheck
	s.ReportIncident(ctx, &pb.IncidentReport{Payload: incidentJSON(inc2)})                                         //nolint:errcheck
	s.DispatchUnit(ctx, &pb.DispatchRequest{Payload: j(map[string]string{"incident_id": inc1, "unit_id": unit1})}) //nolint:errcheck
	pub.failing = 4                                                                                                // the broker is down for the first four publish calls
	total := 0
	for i := 0; i < 10 && total < 3; i++ {
		n, err := ob.Drain(bg, tenantA)
		if err != nil && n < 0 {
			t.Fatal(err)
		}
		total += n
	}
	events, audit := pub.on(EventsTopic), pub.on(AuditTopic)
	if len(events) != 3 || len(audit) != 3 {
		t.Fatalf("each event must reach both topics exactly once: %d events, %d audit", len(events), len(audit))
	}
	for i, r := range events {
		var m struct {
			TenantID    string `json:"tenant_id"`
			SequenceID  int64  `json:"sequence_id"`
			AggregateID string `json:"aggregate_id"`
			EntryHash   string `json:"entry_hash"`
		}
		if err := json.Unmarshal(r.value, &m); err != nil || m.SequenceID != int64(i+1) || m.TenantID != tenantA || r.key != m.AggregateID || len(m.EntryHash) != 64 {
			t.Fatalf("event %d out of order or malformed: %s key=%s", i, r.value, r.key)
		}
		if audit[i].key != strconv.Itoa(i+1) {
			t.Fatalf("audit key must be the sequence id, got %s", audit[i].key)
		}
	}
	if n, _ := ob.Drain(bg, tenantA); n != 0 {
		t.Fatalf("a second drain republished %d events", n)
	}
}

func TestACommandSucceedsWhileTheBrokerIsDownAndCatchesUpLater(t *testing.T) {
	pub := &fakePublisher{failing: 1 << 30}
	s, ob, st := newServer(pub)
	if _, err := s.ReportIncident(as(tenantA, "u"), &pb.IncidentReport{Payload: incidentJSON(inc1)}); err != nil {
		t.Fatalf("the broker being down must not fail the command: %v", err)
	}
	if got, _ := st.Load(bg, tenantA, 0, 0); len(got) != 1 {
		t.Fatal("the event must be in the log")
	}
	if _, err := ob.Drain(bg, tenantA); err == nil {
		t.Fatal("drain must report the broker failure")
	}
	pub.mu.Lock()
	pub.failing = 0
	pub.mu.Unlock()
	if n, err := ob.Drain(bg, tenantA); err != nil || n != 1 {
		t.Fatalf("after the broker heals the event must be published: %d %v", n, err)
	}
}

func TestRunPublishesNotifiedTenants(t *testing.T) {
	pub := &fakePublisher{failing: 2}
	s, ob, _ := newServer(pub)
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	go ob.Run(ctx, 20*time.Millisecond)
	s.ReportIncident(as(tenantA, "u"), &pb.IncidentReport{Payload: incidentJSON(inc1)}) //nolint:errcheck
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(pub.on(EventsTopic)) == 1 && len(pub.on(AuditTopic)) == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("event never published: %d/%d", len(pub.on(EventsTopic)), len(pub.on(AuditTopic)))
}

// The service answers a real gRPC call on the wire, not only direct method calls.
func TestDispatchServiceAnswersARealGrpcCall(t *testing.T) {
	s, _, _ := newServer(&fakePublisher{})
	lis := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	pb.RegisterDispatchServiceServer(g, s)
	go g.Serve(lis) //nolint:errcheck
	defer g.Stop()
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := pb.NewDispatchServiceClient(conn)
	out := metadata.AppendToOutgoingContext(bg, tenantHeader, tenantA, actorHeader, "u1")
	if _, err := c.ReportIncident(out, &pb.IncidentReport{Payload: incidentJSON(inc1)}); err != nil {
		t.Fatalf("report over the wire: %v", err)
	}
	rep, err := c.VerifyChain(out, &pb.VerifyRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var v VerifyResult
	json.Unmarshal(rep.Payload, &v) //nolint:errcheck
	if !v.OK || v.Checked != 1 {
		t.Fatalf("verify over the wire: %s", rep.Payload)
	}
	if _, err := c.VerifyChain(bg, &pb.VerifyRequest{}); code(err) != codes.Unauthenticated {
		t.Fatalf("a call without credentials must be refused: %v", err)
	}
	_ = fmt.Sprint
}

// A restart builds a new Outbox over the SAME cursors (in production: the outbox_cursor table): it must carry on after the last published event.
func TestRestartedOutboxResumesFromTheSavedCursor(t *testing.T) {
	st := NewMemoryStore()
	for i := 0; i < 3; i++ {
		if _, err := st.Append(bg, tenantA, "aaaaaaaa-0000-4000-8000-00000000000"+string(rune('1'+i)), IncidentReported, json.RawMessage(`{}`), "u"); err != nil {
			t.Fatal(err)
		}
	}
	cursors := NewMemCursors()
	first := &fakePublisher{}
	if n, err := NewOutbox(st, first, cursors).Drain(bg, tenantA); err != nil || n != 3 {
		t.Fatalf("first run: %d %v", n, err)
	}
	if _, err := st.Append(bg, tenantA, "aaaaaaaa-0000-4000-8000-000000000009", IncidentReported, json.RawMessage(`{}`), "u"); err != nil {
		t.Fatal(err)
	}
	second := &fakePublisher{}
	if n, err := NewOutbox(st, second, cursors).Drain(bg, tenantA); err != nil || n != 1 {
		t.Fatalf("after restart only the new event goes out: %d %v", n, err)
	}
	if err := cursors.Set(bg, tenantA, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := cursors.Get(bg, tenantA); got != 4 {
		t.Fatalf("a cursor never moves backwards: %d", got)
	}
}
