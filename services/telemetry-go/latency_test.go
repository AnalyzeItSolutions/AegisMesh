package main

import (
	"context"
	"fmt"
	"net"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/aegismesh/aegismesh/gen/aegismeshv1"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	h3 "github.com/uber/h3-go/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

const (
	sosFrames = 10_000
	sosRate   = 2_000 // frames per second sent: a steady stream, so the figure is latency and not the time to drain a burst
)

// SOS to map: a real gRPC Ingest call on the in-process stack, to a WebSocket client subscribed to the viewport, 10,000 times.
// Latency is measured from just before the Ingest call to the moment the client has read the frame.
func TestSOSToMapLatency(t *testing.T) {
	srv := NewServer(&fakeGeo{}, &fakePub{}, NewFanout())

	// gRPC server on an in-memory listener
	lis := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	pb.RegisterTelemetryServiceServer(g, srv)
	go g.Serve(lis) //nolint:errcheck
	defer g.Stop()
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := pb.NewTelemetryServiceClient(conn)

	// WebSocket client over real localhost TCP, viewport = the cell around the incident area and its neighbours
	ts := httptest.NewServer(srv.WSHandler())
	defer ts.Close()
	ctx, cancel := context.WithTimeout(bg, 60*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/", &websocket.DialOptions{HTTPHeader: map[string][]string{tenantHeader: {tenantA}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(1 << 20)
	centre := h3.Cell(cellAt(t, 12.97, 77.59, 7))
	disk, err := centre.GridDisk(1)
	if err != nil {
		t.Fatal(err)
	}
	var viewport []uint64
	for _, c := range disk {
		viewport = append(viewport, uint64(c))
	}
	if err := wsjson.Write(ctx, ws, map[string]any{"type": "viewport", "cells": viewport}); err != nil {
		t.Fatal(err)
	}
	var first wsOut
	if err := wsjson.Read(ctx, ws, &first); err != nil || first.Type != "snapshot" {
		t.Fatalf("expected the snapshot first: %+v %v", first, err)
	}

	base := time.Now()
	sentAt := make([]int64, sosFrames) // nanoseconds since base
	recvAt := make([]int64, sosFrames)
	var received atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for received.Load() < sosFrames {
			var m wsOut
			if err := wsjson.Read(ctx, ws, &m); err != nil {
				return
			}
			if m.Type != "frame" || m.Frame == nil || !strings.HasPrefix(m.Frame.UnitID, "sos-") {
				continue
			}
			i, err := strconv.Atoi(strings.TrimPrefix(m.Frame.UnitID, "sos-"))
			if err != nil || i < 0 || i >= sosFrames {
				continue
			}
			recvAt[i] = int64(time.Since(base))
			received.Add(1)
		}
	}()

	out := metadata.AppendToOutgoingContext(ctx, tenantHeader, tenantA)
	interval := time.Second / sosRate
	start := time.Now()
	for i := 0; i < sosFrames; i++ {
		for time.Since(start) < time.Duration(i)*interval { // pace the sender
		}
		payload := fmt.Sprintf(`{"unit_id":"sos-%d","lat":%.6f,"lon":%.6f,"heading":90,"ts":%d}`, i, 12.9700+float64(i%100)*0.00001, 77.5900+float64(i%97)*0.00001, 1_700_000_000_000+int64(i))
		sentAt[i] = int64(time.Since(base))
		if _, err := client.Ingest(out, &pb.LocationFrame{Payload: []byte(payload)}); err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
	if got := received.Load(); got != sosFrames {
		t.Fatalf("only %d of %d frames reached the map", got, sosFrames)
	}

	lat := make([]float64, sosFrames)
	for i := range lat {
		lat[i] = float64(recvAt[i]-sentAt[i]) / 1e6 // milliseconds
		if lat[i] < 0 {
			t.Fatalf("frame %d arrived before it was sent", i)
		}
	}
	sort.Float64s(lat)
	pct := func(p float64) float64 { return lat[int(float64(len(lat))*p)-1] }
	t.Logf("%d frames at %d/s: p50 %.3f ms, p99 %.3f ms, max %.3f ms", sosFrames, sosRate, pct(0.50), pct(0.99), lat[len(lat)-1])
	fmt.Printf("BENCH sos_to_map_p99 %.3f\n", pct(0.99))
}
