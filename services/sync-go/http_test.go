package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func call(t *testing.T, h http.Handler, method, path, tenant, body string) (int, string, http.Header) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if tenant != "" {
		req.Header.Set(tenantHeader, tenant)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out, _ := io.ReadAll(rec.Body)
	return rec.Code, string(out), rec.Header()
}

const batchJSON = `{"replica_id":"r1","lww":[{"key":"incident:1","value":"flooded","hlc":7340032,"replica_id":"r1"}],"orset_adds":[{"set":"units","element":"u1","tag":"r1:1"}]}`

func TestHTTPPushThenPullRoundTripsAndMatchesTheGrpcSide(t *testing.T) {
	s := NewServer("hub")
	h := s.HTTPHandler()
	code, out, hdr := call(t, h, "POST", "/sync/push", "t1", batchJSON)
	if code != 200 || hdr.Get("Content-Type") != "application/json" || !strings.Contains(out, `"accepted":2`) {
		t.Fatalf("push: %d %s %v", code, out, hdr)
	}
	code, out, _ = call(t, h, "POST", "/sync/pull", "t1", `{"since_seq":0}`)
	var got batch
	if code != 200 || json.Unmarshal([]byte(out), &got) != nil || len(got.LWW) != 1 || got.LWW[0].Value != "flooded" || got.Cursor != 1 || len(got.Adds) != 1 {
		t.Fatalf("pull: %d %s", code, out)
	}
	// the very same state is what the gRPC methods report: one implementation behind both fronts
	if code, hash, _ := call(t, h, "GET", "/sync/hash", "t1", ""); code != 200 || hash != string(mustHash(t, s, "t1")) {
		t.Fatalf("hash differs between HTTP (%d %s) and gRPC", code, hash)
	}
	code, out, _ = call(t, h, "POST", "/sync/pull", "t1", `{"since_seq":1}`)
	if code != 200 || strings.Contains(out, "incident:1") {
		t.Fatalf("a pull past the cursor must not repeat what was already delivered: %s", out)
	}
}

func mustHash(t *testing.T, s *Server, tenant string) []byte {
	t.Helper()
	out, err := s.StateHash(ctxFor(tenant), nil)
	if err != nil {
		t.Fatal(err)
	}
	return out.Payload
}

func TestHTTPTenantsAreIsolatedAndTheTenantComesOnlyFromTheHeader(t *testing.T) {
	h := NewServer("hub").HTTPHandler()
	call(t, h, "POST", "/sync/push", "t1", batchJSON)
	_, out, _ := call(t, h, "POST", "/sync/pull", "t2", `{}`)
	if strings.Contains(out, "incident:1") {
		t.Fatalf("tenant t2 sees tenant t1's data: %s", out)
	}
	// a tenant named inside the body is not a thing: only the header counts
	call(t, h, "POST", "/sync/push", "t2", `{"tenant_id":"t1","lww":[{"key":"sneaky","value":"x","hlc":1,"replica_id":"r"}]}`)
	_, out, _ = call(t, h, "POST", "/sync/pull", "t1", `{}`)
	if strings.Contains(out, "sneaky") {
		t.Fatalf("a body field moved data into another tenant: %s", out)
	}
}

func TestHTTPRefusalsUseTheRightStatusCodes(t *testing.T) {
	h := NewServer("hub").HTTPHandler()
	cases := []struct {
		name, method, path, tenant, body string
		want                             int
	}{
		{"no tenant header", "POST", "/sync/push", "", batchJSON, 401},
		{"no tenant header on pull", "POST", "/sync/pull", "", `{}`, 401},
		{"no tenant header on hash", "GET", "/sync/hash", "", "", 401},
		{"body is not json", "POST", "/sync/push", "t1", "nope", 400},
		{"empty key", "POST", "/sync/push", "t1", `{"lww":[{"key":"","value":"v","hlc":1,"replica_id":"r"}]}`, 400},
		{"pull body is not json", "POST", "/sync/pull", "t1", "nope", 400},
		{"wrong method", "GET", "/sync/push", "t1", "", 405},
		{"unknown path", "POST", "/sync/other", "t1", "{}", 404},
	}
	for _, c := range cases {
		code, out, _ := call(t, h, c.method, c.path, c.tenant, c.body)
		if code != c.want {
			t.Errorf("%s: got %d (%s), want %d", c.name, code, out, c.want)
		}
		if c.want >= 400 && c.want != 404 && c.want != 405 && !strings.Contains(out, `"error"`) {
			t.Errorf("%s: an error must say what was wrong as JSON, got %q", c.name, out)
		}
	}
	if code, out, _ := call(t, h, "GET", "/healthz", "", ""); code != 200 || out != "ok" {
		t.Errorf("health: %d %q", code, out)
	}
}

func TestHTTPRefusesABodyThatIsTooLargeAndChangesNothing(t *testing.T) {
	s := NewServer("hub")
	h := s.HTTPHandler()
	big := `{"lww":[{"key":"k","value":"` + strings.Repeat("x", maxBody) + `","hlc":1,"replica_id":"r"}]}`
	if code, _, _ := call(t, h, "POST", "/sync/push", "t1", big); code != 413 {
		t.Fatalf("expected 413, got %d", code)
	}
	if _, out, _ := call(t, h, "POST", "/sync/pull", "t1", `{}`); strings.Contains(out, `"key":"k"`) {
		t.Fatal("a refused request was applied")
	}
}

func TestHTTPClientsConvergeThroughTheServer(t *testing.T) {
	h := NewServer("hub").HTTPHandler()
	a := `{"replica_id":"a","lww":[{"key":"k","value":"by-a","hlc":100,"replica_id":"a"}]}`
	b := `{"replica_id":"b","lww":[{"key":"k","value":"by-b","hlc":100,"replica_id":"b"}]}`
	for _, body := range []string{a, b, a, b} { // either order, repeated: the same result
		call(t, h, "POST", "/sync/push", "t1", body)
	}
	_, out, _ := call(t, h, "POST", "/sync/pull", "t1", `{}`)
	if !bytes.Contains([]byte(out), []byte(`"value":"by-b"`)) {
		t.Fatalf("the higher replica id must win on a tie: %s", out)
	}
}
