package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	pb "github.com/aegismesh/aegismesh/gen/aegismeshv1"
)

func get(t *testing.T, h http.Handler, path, tenant string) (int, string, http.Header) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if tenant != "" {
		req.Header.Set(tenantHeader, tenant)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out, _ := io.ReadAll(rec.Body)
	return rec.Code, string(out), rec.Header()
}

func serverWithEvents(t *testing.T, n int) *Server {
	t.Helper()
	s, _, _ := newServer(&fakePublisher{})
	ctx := as(tenantA, "u1")
	for i := 0; i < n; i++ {
		id := "aaaaaaaa-0000-4000-8000-" + strings.Repeat("0", 11) + strconv.Itoa(i%10)
		if i >= 10 {
			id = "aaaaaaaa-0000-4000-8000-0000000000" + strconv.Itoa(i)
		}
		payload := j(map[string]any{"id": id, "kind": "flood", "severity": "red", "lat": 12.97, "lon": 77.59, "h3_cell": 618683397703204863, "reported_at": 1700000000000 + i})
		if _, err := s.ReportIncident(ctx, &pb.IncidentReport{Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestEventsAreServedInOrderWithExactCellIdsAndTheContractShape(t *testing.T) {
	s := serverWithEvents(t, 3)
	code, out, hdr := get(t, s.HTTPHandler(), "/events", tenantA)
	if code != 200 || hdr.Get("Content-Type") != "application/json" || hdr.Get("X-Next-After") != "3" {
		t.Fatalf("%d %v", code, hdr)
	}
	var evs []map[string]any
	if err := json.Unmarshal([]byte(out), &evs); err != nil || len(evs) != 3 {
		t.Fatalf("%v %s", err, out)
	}
	for i, e := range evs {
		if int(e["sequence_id"].(float64)) != i+1 || e["tenant_id"] != tenantA || e["event_type"] != "IncidentReported" || len(e["entry_hash"].(string)) != 64 || e["actor_id"] != "u1" {
			t.Fatalf("event %d is not the contract's shape: %v", i, e)
		}
	}
	if !strings.Contains(out, `"h3_cell":618683397703204863`) {
		t.Fatalf("a 64-bit cell id must stay exact digits in the log, got: %.300s", out)
	}
}

func TestEventsPageWithoutGapsOrRepeats(t *testing.T) {
	s := serverWithEvents(t, 7)
	h := s.HTTPHandler()
	var seen []int
	after := "0"
	for page := 0; page < 10; page++ {
		code, out, hdr := get(t, h, "/events?limit=3&after="+after, tenantA)
		if code != 200 {
			t.Fatalf("%d %s", code, out)
		}
		var evs []struct {
			Seq int `json:"sequence_id"`
		}
		_ = json.Unmarshal([]byte(out), &evs)
		for _, e := range evs {
			seen = append(seen, e.Seq)
		}
		after = hdr.Get("X-Next-After")
		if len(evs) < 3 {
			break
		}
	}
	want := []int{1, 2, 3, 4, 5, 6, 7}
	if len(seen) != 7 {
		t.Fatalf("paging gave %v", seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("paging gave %v, want %v", seen, want)
		}
	}
	if code, out, hdr := get(t, h, "/events?after=7", tenantA); code != 200 || strings.TrimSpace(out) != "[]" || hdr.Get("X-Next-After") != "7" {
		t.Fatalf("past the end: %d %q %q", code, out, hdr.Get("X-Next-After"))
	}
}

func TestEventsAreIsolatedByTenantAndRefuseBadRequests(t *testing.T) {
	s := serverWithEvents(t, 2)
	h := s.HTTPHandler()
	if _, out, _ := get(t, h, "/events", tenantB); strings.TrimSpace(out) != "[]" {
		t.Fatalf("tenant B sees tenant A's events: %s", out)
	}
	cases := []struct {
		name, path, tenant string
		want               int
	}{
		{"no tenant", "/events", "", 401},
		{"tenant is not a uuid", "/events", "not-a-uuid", 400},
		{"after is not a number", "/events?after=x", tenantA, 400},
		{"after is negative", "/events?after=-1", tenantA, 400},
		{"limit is zero", "/events?limit=0", tenantA, 400},
		{"limit is not a number", "/events?limit=many", tenantA, 400},
	}
	for _, c := range cases {
		if code, out, _ := get(t, h, c.path, c.tenant); code != c.want || (c.want != 401 && !strings.Contains(out, `"error"`)) {
			t.Errorf("%s: got %d %q, want %d", c.name, code, out, c.want)
		}
	}
	req := httptest.NewRequest("POST", "/events", nil)
	req.Header.Set(tenantHeader, tenantA)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 405 {
		t.Errorf("the log is read-only over HTTP: POST got %d", rec.Code)
	}
	if code, out, _ := get(t, h, "/healthz", ""); code != 200 || out != "ok" {
		t.Errorf("health: %d %q", code, out)
	}
}

func TestAHugeLimitIsCappedNotHonoured(t *testing.T) {
	s := serverWithEvents(t, 2)
	if code, _, _ := get(t, s.HTTPHandler(), "/events?limit=99999999", tenantA); code != 200 {
		t.Fatalf("a large limit should be capped, got %d", code)
	}
}
