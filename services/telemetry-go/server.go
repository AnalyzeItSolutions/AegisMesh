package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	pb "github.com/aegismesh/aegismesh/gen/aegismeshv1"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	tenantHeader   = "x-tenant-id"
	LocationsTopic = "telemetry.unit.locations"
)

var errInvalid = errors.New("invalid argument")

// GeoStore keeps each unit's latest position.
type GeoStore interface {
	Put(ctx context.Context, tenant string, f Frame) error
}

// Publisher sends one record to the log; it must return an error if the record was not accepted.
type Publisher interface {
	Publish(ctx context.Context, topic, key string, value []byte) error
}

// ---- Redis: latest positions as a geo set, and frames over pub/sub so every instance can feed its own WebSocket clients ----

type RedisGeo struct {
	rdb      redis.UniversalClient
	instance string
}

func NewRedisGeo(rdb redis.UniversalClient, instance string) *RedisGeo {
	return &RedisGeo{rdb: rdb, instance: instance}
}

type envelope struct {
	Origin string `json:"origin"`
	Tenant string `json:"tenant_id"`
	Frame  Frame  `json:"frame"`
}

func (r *RedisGeo) Put(ctx context.Context, tenant string, f Frame) error {
	msg, err := json.Marshal(envelope{Origin: r.instance, Tenant: tenant, Frame: f})
	if err != nil {
		return err
	}
	pipe := r.rdb.Pipeline()
	pipe.GeoAdd(ctx, "geo:"+tenant+":units", &redis.GeoLocation{Name: f.UnitID, Longitude: f.Lon, Latitude: f.Lat})
	pipe.Publish(ctx, "frames:"+tenant, msg)
	_, err = pipe.Exec(ctx)
	return err
}

// Listen feeds frames published by OTHER instances into the local fan-out until ctx ends (an instance's own frames were delivered directly).
func (r *RedisGeo) Listen(ctx context.Context, fan *Fanout) error {
	ps := r.rdb.PSubscribe(ctx, "frames:*")
	defer ps.Close()
	if _, err := ps.Receive(ctx); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case m, ok := <-ps.Channel():
			if !ok {
				return nil
			}
			var e envelope
			if json.Unmarshal([]byte(m.Payload), &e) != nil || e.Origin == r.instance || e.Tenant == "" || e.Frame.UnitID == "" {
				continue
			}
			fan.Publish(e.Tenant, e.Frame)
		}
	}
}

// ---- Redpanda ----

type KafkaPublisher struct{ cl *kgo.Client }

func NewKafkaPublisher(brokers []string) (*KafkaPublisher, error) {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.RequiredAcks(kgo.AllISRAcks()))
	if err != nil {
		return nil, err
	}
	return &KafkaPublisher{cl: cl}, nil
}

func (k *KafkaPublisher) Publish(ctx context.Context, topic, key string, value []byte) error {
	return k.cl.ProduceSync(ctx, &kgo.Record{Topic: topic, Key: []byte(key), Value: value}).FirstErr()
}

func (k *KafkaPublisher) Close() { k.cl.Close() }

// ---- the service ----

// Server implements TelemetryService and the WebSocket endpoint on one Fanout.
type Server struct {
	pb.UnimplementedTelemetryServiceServer
	geo  GeoStore
	pub  Publisher
	fan  *Fanout
	now  func() time.Time
	mu   sync.Mutex
	last map[string]int64 // tenant/unit -> timestamp of the newest frame accepted
}

func NewServer(geo GeoStore, pub Publisher, fan *Fanout) *Server {
	return &Server{geo: geo, pub: pub, fan: fan, now: time.Now, last: map[string]int64{}}
}

func tenantOf(ctx context.Context, fromRequest string) (string, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	v := md.Get(tenantHeader)
	if len(v) == 0 || v[0] == "" {
		return "", status.Error(codes.Unauthenticated, "missing tenant credentials")
	}
	if fromRequest != "" && fromRequest != v[0] {
		return "", status.Error(codes.InvalidArgument, "tenant_id does not match the credentials")
	}
	return v[0], nil
}

func decodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", errInvalid, err)
	}
	return nil
}

type ingestInput struct {
	UnitID  string   `json:"unit_id"`
	Lat     *float64 `json:"lat"`
	Lon     *float64 `json:"lon"`
	Heading float64  `json:"heading"`
	TS      int64    `json:"ts"`
}

// Ingest validates a position, delivers it to the viewport subscribers first (the fast path), then stores it and publishes it to the log.
// A frame older than the newest one already accepted for the unit is dropped as stale.
func (s *Server) Ingest(ctx context.Context, in *pb.LocationFrame) (*pb.TelemetryAck, error) {
	tenant, err := tenantOf(ctx, in.GetTenantId())
	if err != nil {
		return nil, err
	}
	var req ingestInput
	if err := decodeStrict(in.GetPayload(), &req); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	switch {
	case req.UnitID == "" || len(req.UnitID) > 64:
		return nil, status.Error(codes.InvalidArgument, "unit_id is required (at most 64 characters)")
	case req.Lat == nil || req.Lon == nil || *req.Lat < -90 || *req.Lat > 90 || *req.Lon < -180 || *req.Lon > 180:
		return nil, status.Error(codes.InvalidArgument, "lat and lon are required and must be valid coordinates")
	case req.Heading < 0 || req.Heading > 360:
		return nil, status.Error(codes.InvalidArgument, "heading must be between 0 and 360 degrees")
	case req.TS < 0:
		return nil, status.Error(codes.InvalidArgument, "ts must be milliseconds since the epoch")
	}
	if req.TS == 0 {
		req.TS = s.now().UnixMilli()
	}
	cell, err := cellFor(*req.Lat, *req.Lon)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "position cannot be indexed")
	}
	fr := Frame{UnitID: req.UnitID, Lat: *req.Lat, Lon: *req.Lon, H3Cell: cell, Heading: req.Heading, TS: req.TS}

	key := tenant + "/" + fr.UnitID
	s.mu.Lock()
	stale := req.TS < s.last[key]
	if !stale {
		s.last[key] = req.TS
	}
	s.mu.Unlock()
	if stale {
		return ack(false, true, cell), nil
	}
	s.fan.Publish(tenant, fr)
	value, _ := json.Marshal(fr)
	if err := s.geo.Put(ctx, tenant, fr); err != nil {
		log.Printf("geo store: %v", err)
		return nil, status.Error(codes.Unavailable, "could not store the position")
	}
	if err := s.pub.Publish(ctx, LocationsTopic, fr.UnitID, value); err != nil {
		log.Printf("publish: %v", err)
		return nil, status.Error(codes.Unavailable, "could not publish the position")
	}
	return ack(true, false, cell), nil
}

func ack(accepted, stale bool, cell uint64) *pb.TelemetryAck {
	b, _ := json.Marshal(map[string]any{"accepted": accepted, "stale": stale, "h3_cell": cell})
	return &pb.TelemetryAck{Payload: b}
}

// Subscribe answers a viewport with the latest known position of every unit inside it (a snapshot). Live frames come over the WebSocket:
// the generated unary RPC cannot stream.
func (s *Server) Subscribe(ctx context.Context, in *pb.ViewportRequest) (*pb.LocationFrame, error) {
	tenant, err := tenantOf(ctx, in.GetTenantId())
	if err != nil {
		return nil, err
	}
	var req struct {
		Cells []uint64 `json:"cells"`
	}
	if err := decodeStrict(in.GetPayload(), &req); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := checkCells(req.Cells); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	b, _ := json.Marshal(map[string]any{"frames": s.fan.Snapshot(tenant, req.Cells)})
	return &pb.LocationFrame{TenantId: tenant, Payload: b}, nil
}

// ---- WebSocket: the command center's live feed ----

type wsIn struct {
	Type  string   `json:"type"`
	Cells []uint64 `json:"cells"`
}

type wsOut struct {
	Type    string  `json:"type"`
	Frame   *Frame  `json:"frame,omitempty"`
	Frames  []Frame `json:"frames,omitempty"`
	Message string  `json:"message,omitempty"`
}

// WSHandler serves /ws. The client sends {"type":"viewport","cells":[...]} (again whenever the map moves). The server answers each viewport with
// {"type":"snapshot","frames":[...]} (where the units in it are now), then streams {"type":"frame","frame":{...}} for units that move in it, and
// {"type":"error","message":"..."} for a bad request. The tenant comes from the gateway's x-tenant-id header, which Envoy sets after validating the JWT.
func (s *Server) WSHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenant := strings.TrimSpace(r.Header.Get(tenantHeader))
		if tenant == "" {
			http.Error(w, "missing tenant credentials", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true}) // origin is the gateway's concern; auth is the verified header
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(1 << 20)
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		sub := s.fan.Subscribe(tenant, 256)
		defer sub.Close()

		go func() { // reader: viewport messages
			defer cancel()
			for {
				var m wsIn
				if err := wsjson.Read(ctx, c, &m); err != nil {
					return
				}
				if m.Type != "viewport" {
					_ = wsjson.Write(ctx, c, wsOut{Type: "error", Message: `expected {"type":"viewport","cells":[...]}`})
					continue
				}
				if err := sub.SetCells(m.Cells); err != nil {
					_ = wsjson.Write(ctx, c, wsOut{Type: "error", Message: err.Error()})
					continue
				}
				_ = wsjson.Write(ctx, c, wsOut{Type: "snapshot", Frames: s.fan.Snapshot(tenant, m.Cells)})
			}
		}()
		for { // writer: live frames
			select {
			case <-ctx.Done():
				c.Close(websocket.StatusNormalClosure, "")
				return
			case fr, ok := <-sub.C:
				if !ok {
					return
				}
				if err := wsjson.Write(ctx, c, wsOut{Type: "frame", Frame: &fr}); err != nil {
					return
				}
			}
		}
	}
}
