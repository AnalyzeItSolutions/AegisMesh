package main

import (
	"encoding/json"
	"net/http"
	"strconv"
)

const (
	defaultEventsPage = 5000
	maxEventsPage     = 20000
)

// HTTPHandler is the read side for the browser: the command center loads the event log from here to scrub back in time. As on the gRPC side the tenant
// comes only from the x-tenant-id header the gateway sets after validating the token.
//
//	GET /events?after=N&limit=M   the tenant's events with sequence_id > N, oldest first, at most M (default 5000, at most 20000), as a JSON array of
//	                              {tenant_id, sequence_id, aggregate_id, event_type, payload, actor_id, prev_hash, entry_hash}. The response header
//	                              X-Next-After is the last sequence id returned: ask again with after=<that> until a page comes back shorter than the limit.
//	GET /healthz
func (s *Server) HTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", s.httpEvents)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}

func httpError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func (s *Server) httpEvents(w http.ResponseWriter, r *http.Request) {
	tenant := r.Header.Get(tenantHeader)
	if tenant == "" {
		httpError(w, http.StatusUnauthorized, "missing tenant credentials")
		return
	}
	if !uuidRe.MatchString(tenant) {
		httpError(w, http.StatusBadRequest, "tenant must be a uuid")
		return
	}
	after, limit := int64(0), defaultEventsPage
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			httpError(w, http.StatusBadRequest, "after must be a non-negative integer")
			return
		}
		after = n
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			httpError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = min(n, maxEventsPage)
	}
	events, err := s.d.store.Load(r.Context(), tenant, after, limit)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "could not read the event log")
		return
	}
	out := make([]outboxEvent, 0, len(events))
	for _, e := range events {
		out = append(out, outboxEvent{TenantID: tenant, Entry: e})
	}
	next := after
	if len(events) > 0 {
		next = events[len(events)-1].SequenceID
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Next-After", strconv.FormatInt(next, 10))
	_ = json.NewEncoder(w).Encode(out)
}
