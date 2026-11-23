package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/aegismesh/aegismesh/gen/aegismeshv1"
	"github.com/alicebob/miniredis/v2"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/redis/go-redis/v9"
	h3 "github.com/uber/h3-go/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	tenantA = "11111111-1111-4111-8111-111111111111"
	tenantB = "22222222-2222-4222-8222-222222222222"
)

var bg = context.Background()

type fakeGeo struct {
	mu   sync.Mutex
	puts []Frame
	err  error
}

func (g *fakeGeo) Put(_ context.Context, _ string, f Frame) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return g.err
	}
	g.puts = append(g.puts, f)
	return nil
}

type fakePub struct {
	mu   sync.Mutex
	recs []struct {
		topic, key string
		value      []byte
	}
	err error
}

func (p *fakePub) Publish(_ context.Context, topic, key string, value []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.recs = append(p.recs, struct {
		topic, key string
		value      []byte
	}{topic, key, value})
	return nil
}

func newServer() (*Server, *fakeGeo, *fakePub) {
	g, p := &fakeGeo{}, &fakePub{}
	return NewServer(g, p, NewFanout()), g, p
}

func as(tenant string) context.Context {
	return metadata.NewIncomingContext(bg, metadata.Pairs(tenantHeader, tenant))
}

func j(v any) []byte { b, _ := json.Marshal(v); return b }

func ingest(t *testing.T, s *Server, tenant string, doc map[string]any) {
	t.Helper()
	if _, err := s.Ingest(as(tenant), &pb.LocationFrame{Payload: j(doc)}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
}

func cellAt(t *testing.T, lat, lon float64, res int) uint64 {
	t.Helper()
	c, err := h3.LatLngToCell(h3.NewLatLng(lat, lon), res)
	if err != nil {
		t.Fatal(err)
	}
	return uint64(c)
}

func TestIngestIndexesStoresAndPublishesAFrame(t *testing.T) {
	s, geo, pub := newServer()
	out, err := s.Ingest(as(tenantA), &pb.LocationFrame{Payload: j(map[string]any{"unit_id": "u1", "lat": 12.97, "lon": 77.59, "heading": 90.5, "ts": 1700000000000})})
	if err != nil {
		t.Fatal(err)
	}
	var a struct {
		Accepted bool   `json:"accepted"`
		Stale    bool   `json:"stale"`
		H3Cell   uint64 `json:"h3_cell"`
	}
	json.Unmarshal(out.Payload, &a) //nolint:errcheck
	want := cellAt(t, 12.97, 77.59, FrameRes)
	if !a.Accepted || a.Stale || a.H3Cell != want {
		t.Fatalf("ack: %s", out.Payload)
	}
	if len(geo.puts) != 1 || geo.puts[0].H3Cell != want || geo.puts[0].Heading != 90.5 {
		t.Fatalf("geo store: %+v", geo.puts)
	}
	if len(pub.recs) != 1 || pub.recs[0].topic != "telemetry.unit.locations" || pub.recs[0].key != "u1" {
		t.Fatalf("published: %+v", pub.recs)
	}
	var fr Frame
	if err := json.Unmarshal(pub.recs[0].value, &fr); err != nil || fr.UnitID != "u1" || fr.TS != 1700000000000 || fr.H3Cell != want {
		t.Fatalf("frame on the log: %s", pub.recs[0].value)
	}
}

func TestIngestRefusals(t *testing.T) {
	s, geo, pub := newServer()
	call := func(ctx context.Context, tenantInReq string, payload string) codes.Code {
		_, err := s.Ingest(ctx, &pb.LocationFrame{TenantId: tenantInReq, Payload: []byte(payload)})
		return status.Code(err)
	}
	good := `{"unit_id":"u1","lat":1,"lon":2}`
	cases := []struct {
		name string
		got  codes.Code
		want codes.Code
	}{
		{"no credentials", call(bg, "", good), codes.Unauthenticated},
		{"tenant mismatch", call(as(tenantA), tenantB, good), codes.InvalidArgument},
		{"not json", call(as(tenantA), "", "nope"), codes.InvalidArgument},
		{"unknown field", call(as(tenantA), "", `{"unit_id":"u1","lat":1,"lon":2,"speed":9}`), codes.InvalidArgument},
		{"no unit", call(as(tenantA), "", `{"lat":1,"lon":2}`), codes.InvalidArgument},
		{"no lat", call(as(tenantA), "", `{"unit_id":"u","lon":2}`), codes.InvalidArgument},
		{"lat out of range", call(as(tenantA), "", `{"unit_id":"u","lat":91,"lon":2}`), codes.InvalidArgument},
		{"lon out of range", call(as(tenantA), "", `{"unit_id":"u","lat":1,"lon":-181}`), codes.InvalidArgument},
		{"heading out of range", call(as(tenantA), "", `{"unit_id":"u","lat":1,"lon":2,"heading":361}`), codes.InvalidArgument},
		{"negative ts", call(as(tenantA), "", `{"unit_id":"u","lat":1,"lon":2,"ts":-5}`), codes.InvalidArgument},
		{"unit id too long", call(as(tenantA), "", `{"unit_id":"`+strings.Repeat("x", 65)+`","lat":1,"lon":2}`), codes.InvalidArgument},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
	if len(geo.puts) != 0 || len(pub.recs) != 0 {
		t.Fatal("a refused frame reached the store or the log")
	}
	// the equator and the antimeridian are valid positions, not "missing"
	ingest(t, s, tenantA, map[string]any{"unit_id": "z", "lat": 0, "lon": 0})
}

func TestAStaleFrameIsDroppedAndNeverReachesAnyone(t *testing.T) {
	s, geo, pub := newServer()
	sub := s.fan.Subscribe(tenantA, 8)
	sub.SetCells([]uint64{cellAt(t, 12.97, 77.59, 7)}) //nolint:errcheck
	ingest(t, s, tenantA, map[string]any{"unit_id": "u1", "lat": 12.97, "lon": 77.59, "ts": 2000})
	out, err := s.Ingest(as(tenantA), &pb.LocationFrame{Payload: j(map[string]any{"unit_id": "u1", "lat": 12.98, "lon": 77.60, "ts": 1000})})
	if err != nil || !strings.Contains(string(out.Payload), `"stale":true`) || !strings.Contains(string(out.Payload), `"accepted":false`) {
		t.Fatalf("older frame: %s %v", out.Payload, err)
	}
	if len(geo.puts) != 1 || len(pub.recs) != 1 || len(sub.C) != 1 {
		t.Fatalf("the stale frame leaked: geo=%d log=%d subscriber=%d", len(geo.puts), len(pub.recs), len(sub.C))
	}
	ingest(t, s, tenantA, map[string]any{"unit_id": "u1", "lat": 12.97, "lon": 77.59, "ts": 2000}) // an equal timestamp is accepted
}

func TestBackendFailuresAreUnavailableNotInternal(t *testing.T) {
	s, geo, pub := newServer()
	geo.err = errors.New("redis down")
	_, err := s.Ingest(as(tenantA), &pb.LocationFrame{Payload: j(map[string]any{"unit_id": "u", "lat": 1, "lon": 2})})
	if status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "redis") {
		t.Fatalf("geo failure: %v", err)
	}
	geo.err, pub.err = nil, errors.New("broker down")
	_, err = s.Ingest(as(tenantA), &pb.LocationFrame{Payload: j(map[string]any{"unit_id": "u2", "lat": 1, "lon": 2})})
	if status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "broker") {
		t.Fatalf("publish failure: %v", err)
	}
}

func recv(t *testing.T, s *Sub) (Frame, bool) {
	t.Helper()
	select {
	case f := <-s.C:
		return f, true
	case <-time.After(150 * time.Millisecond):
		return Frame{}, false
	}
}

func TestFanoutDeliversByViewportCellAcrossResolutionsAndTenants(t *testing.T) {
	f := NewFanout()
	here := Frame{UnitID: "u1", Lat: 12.97, Lon: 77.59, H3Cell: cellAt(t, 12.97, 77.59, FrameRes)}
	far := Frame{UnitID: "u2", Lat: 28.61, Lon: 77.21, H3Cell: cellAt(t, 28.61, 77.21, FrameRes)}

	coarse, exact, wrongTenant, elsewhere := f.Subscribe(tenantA, 8), f.Subscribe(tenantA, 8), f.Subscribe(tenantB, 8), f.Subscribe(tenantA, 8)
	for sub, cells := range map[*Sub][]uint64{
		coarse:      {cellAt(t, 12.97, 77.59, 6)}, // a coarse viewport cell contains the frame's cell
		exact:       {here.H3Cell},                // the frame's own cell
		wrongTenant: {cellAt(t, 12.97, 77.59, 6)},
		elsewhere:   {far.H3Cell},
	} {
		if err := sub.SetCells(cells); err != nil {
			t.Fatal(err)
		}
	}
	f.Publish(tenantA, here)
	for name, sub := range map[string]*Sub{"coarse": coarse, "exact": exact} {
		if got, ok := recv(t, sub); !ok || got != here {
			t.Errorf("%s subscriber: %+v %v", name, got, ok)
		}
	}
	if _, ok := recv(t, wrongTenant); ok {
		t.Error("tenant B received tenant A's frame")
	}
	if _, ok := recv(t, elsewhere); ok {
		t.Error("a viewport over Delhi received a frame from Bangalore")
	}
}

func TestMovingTheViewportAndClosingStopDelivery(t *testing.T) {
	f := NewFanout()
	fr := Frame{UnitID: "u1", H3Cell: cellAt(t, 12.97, 77.59, FrameRes), Lat: 12.97, Lon: 77.59}
	s := f.Subscribe(tenantA, 8)
	s.SetCells([]uint64{cellAt(t, 12.97, 77.59, 7)}) //nolint:errcheck
	s.SetCells([]uint64{cellAt(t, 28.61, 77.21, 7)}) //nolint:errcheck // the map moved to Delhi: the old viewport no longer counts
	f.Publish(tenantA, fr)
	if _, ok := recv(t, s); ok {
		t.Fatal("delivered to a viewport that was replaced")
	}
	s.SetCells([]uint64{cellAt(t, 12.97, 77.59, 7)}) //nolint:errcheck
	s.Close()
	s.Close() // closing twice is harmless
	f.Publish(tenantA, fr)
	if _, open := <-s.C; open {
		t.Fatal("channel should be closed")
	}
	if len(f.index) != 0 {
		t.Fatalf("closed subscribers must leave no index entries: %v", f.index)
	}
	if err := s.SetCells(nil); err == nil {
		t.Fatal("a closed subscription cannot be reused")
	}
}

func TestACellFinerThanAFrameOrInvalidIsRefused(t *testing.T) {
	s := NewFanout().Subscribe(tenantA, 1)
	for name, cells := range map[string][]uint64{"finer": {cellAt(t, 1, 1, 12)}, "invalid": {12345}, "too many": make([]uint64, MaxViewportCells+1)} {
		if err := s.SetCells(cells); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestASlowSubscriberKeepsTheNewestFramesAndCountsWhatItLost(t *testing.T) {
	f := NewFanout()
	s := f.Subscribe(tenantA, 3)
	s.SetCells([]uint64{cellAt(t, 12.97, 77.59, 7)}) //nolint:errcheck
	for i := 1; i <= 10; i++ {
		f.Publish(tenantA, Frame{UnitID: "u", H3Cell: cellAt(t, 12.97, 77.59, FrameRes), TS: int64(i)})
	}
	if s.Dropped() != 7 || len(s.C) != 3 {
		t.Fatalf("dropped=%d buffered=%d", s.Dropped(), len(s.C))
	}
	var ts []int64
	for len(s.C) > 0 {
		ts = append(ts, (<-s.C).TS)
	}
	if len(ts) != 3 || ts[0] != 8 || ts[2] != 10 {
		t.Fatalf("expected the newest three, got %v", ts)
	}
}

func TestSnapshotAndTheSubscribeRPCReturnWhereUnitsAreNow(t *testing.T) {
	s, _, _ := newServer()
	ingest(t, s, tenantA, map[string]any{"unit_id": "b", "lat": 12.97, "lon": 77.59, "ts": 10})
	ingest(t, s, tenantA, map[string]any{"unit_id": "a", "lat": 12.9701, "lon": 77.5901, "ts": 10})
	ingest(t, s, tenantA, map[string]any{"unit_id": "a", "lat": 12.9702, "lon": 77.5902, "ts": 20}) // a moved: only its latest counts
	ingest(t, s, tenantA, map[string]any{"unit_id": "delhi", "lat": 28.61, "lon": 77.21})
	ingest(t, s, tenantB, map[string]any{"unit_id": "other", "lat": 12.97, "lon": 77.59})
	out, err := s.Subscribe(as(tenantA), &pb.ViewportRequest{Payload: j(map[string]any{"cells": []uint64{cellAt(t, 12.97, 77.59, 7)}})})
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Frames []Frame }
	json.Unmarshal(out.Payload, &got) //nolint:errcheck
	if len(got.Frames) != 2 || got.Frames[0].UnitID != "a" || got.Frames[0].TS != 20 || got.Frames[1].UnitID != "b" {
		t.Fatalf("snapshot: %s", out.Payload)
	}
	if _, err := s.Subscribe(as(tenantA), &pb.ViewportRequest{Payload: []byte(`{"cells":[1]}`)}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad cell: %v", err)
	}
	if _, err := s.Subscribe(bg, &pb.ViewportRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no credentials: %v", err)
	}
}

func dial(t *testing.T, s *Server, tenant string) (*websocket.Conn, context.Context) {
	t.Helper()
	ts := httptest.NewServer(s.WSHandler())
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(bg, 5*time.Second)
	t.Cleanup(cancel)
	hdr := map[string][]string{}
	if tenant != "" {
		hdr[tenantHeader] = []string{tenant}
	}
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/", &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c, ctx
}

func readMsg(t *testing.T, ctx context.Context, c *websocket.Conn) wsOut {
	t.Helper()
	var m wsOut
	if err := wsjson.Read(ctx, c, &m); err != nil {
		t.Fatalf("read: %v", err)
	}
	return m
}

func TestWebSocketViewportSnapshotThenLiveFrames(t *testing.T) {
	s, _, _ := newServer()
	ingest(t, s, tenantA, map[string]any{"unit_id": "u1", "lat": 12.97, "lon": 77.59, "ts": 10})
	c, ctx := dial(t, s, tenantA)
	if err := wsjson.Write(ctx, c, map[string]any{"type": "viewport", "cells": []uint64{cellAt(t, 12.97, 77.59, 7)}}); err != nil {
		t.Fatal(err)
	}
	if m := readMsg(t, ctx, c); m.Type != "snapshot" || len(m.Frames) != 1 || m.Frames[0].UnitID != "u1" {
		t.Fatalf("snapshot: %+v", m)
	}
	ingest(t, s, tenantA, map[string]any{"unit_id": "u1", "lat": 12.9702, "lon": 77.5902, "ts": 20})
	ingest(t, s, tenantA, map[string]any{"unit_id": "far", "lat": 28.61, "lon": 77.21, "ts": 21}) // outside the viewport
	ingest(t, s, tenantB, map[string]any{"unit_id": "spy", "lat": 12.97, "lon": 77.59, "ts": 22}) // another tenant
	if m := readMsg(t, ctx, c); m.Type != "frame" || m.Frame == nil || m.Frame.UnitID != "u1" || m.Frame.TS != 20 {
		t.Fatalf("live frame: %+v", m)
	}
	ingest(t, s, tenantA, map[string]any{"unit_id": "u2", "lat": 12.97, "lon": 77.59, "ts": 30})
	if m := readMsg(t, ctx, c); m.Frame == nil || m.Frame.UnitID != "u2" {
		t.Fatalf("the next frame must be u2, not one from outside the viewport or another tenant: %+v", m)
	}
}

func TestWebSocketRefusesBadRequests(t *testing.T) {
	s, _, _ := newServer()
	ts := httptest.NewServer(s.WSHandler())
	defer ts.Close()
	ctx, cancel := context.WithTimeout(bg, 5*time.Second)
	defer cancel()
	if _, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/", nil); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("a connection without credentials must get 401: %v", err)
	}
	c, ctx2 := dial(t, s, tenantA)
	wsjson.Write(ctx2, c, map[string]any{"type": "hello"}) //nolint:errcheck
	if m := readMsg(t, ctx2, c); m.Type != "error" {
		t.Fatalf("unknown message type: %+v", m)
	}
	wsjson.Write(ctx2, c, map[string]any{"type": "viewport", "cells": []uint64{7}}) //nolint:errcheck
	if m := readMsg(t, ctx2, c); m.Type != "error" || m.Message == "" {
		t.Fatalf("bad cell: %+v", m)
	}
}

func TestRedisStoresTheLatestPositionAndFeedsOtherInstancesOnly(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	fanA, fanB := NewFanout(), NewFanout()
	a, b := NewRedisGeo(rdb, "instance-a"), NewRedisGeo(redis.NewClient(&redis.Options{Addr: mr.Addr()}), "instance-b")
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	for _, l := range []struct {
		g *RedisGeo
		f *Fanout
	}{{a, fanA}, {b, fanB}} {
		go l.g.Listen(ctx, l.f) //nolint:errcheck
	}
	time.Sleep(150 * time.Millisecond) // both feeds subscribed

	sa := NewServer(a, &fakePub{}, fanA)
	subA, subB := fanA.Subscribe(tenantA, 8), fanB.Subscribe(tenantA, 8)
	cell7 := []uint64{cellAt(t, 12.97, 77.59, 7)}
	subA.SetCells(cell7) //nolint:errcheck
	subB.SetCells(cell7) //nolint:errcheck

	ingest(t, sa, tenantA, map[string]any{"unit_id": "u1", "lat": 12.97, "lon": 77.59, "ts": 5})
	if f, ok := recv(t, subA); !ok || f.UnitID != "u1" {
		t.Fatalf("instance A's own subscriber: %+v %v", f, ok)
	}
	if f, ok := recv(t, subB); !ok || f.UnitID != "u1" {
		t.Fatalf("instance B's subscriber must get it through Redis: %+v %v", f, ok)
	}
	if _, ok := recv(t, subA); ok {
		t.Fatal("instance A delivered its own frame twice (directly and from Redis)")
	}
	pos, err := rdb.GeoPos(bg, "geo:"+tenantA+":units", "u1").Result()
	if err != nil || len(pos) != 1 || pos[0] == nil || pos[0].Latitude < 12.96 || pos[0].Latitude > 12.98 || pos[0].Longitude < 77.58 || pos[0].Longitude > 77.60 {
		t.Fatalf("geo set: %+v %v", pos, err)
	}
	if mr.Exists("geo:" + tenantB + ":units") {
		t.Fatal("tenant B's geo set must not exist")
	}
}
