package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	pb "github.com/aegismesh/aegismesh/gen/aegismeshv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	hashchain "snippet/hashchain"
)

// The gateway verifies the JWT and forwards who is calling; the tenant and the actor never come from the payload.
const (
	tenantHeader = "x-tenant-id"
	actorHeader  = "x-actor-id"
)

// Server implements DispatchService on top of the Dispatcher; every committed event is handed to the Outbox.
type Server struct {
	pb.UnimplementedDispatchServiceServer
	d  *Dispatcher
	ob *Outbox
}

func NewServer(d *Dispatcher, ob *Outbox) *Server { return &Server{d: d, ob: ob} }

func identity(ctx context.Context, fromRequest string) (tenant, actor string, err error) {
	md, _ := metadata.FromIncomingContext(ctx)
	t, a := md.Get(tenantHeader), md.Get(actorHeader)
	if len(t) == 0 || t[0] == "" || len(a) == 0 || a[0] == "" {
		return "", "", status.Error(codes.Unauthenticated, "missing tenant or actor credentials")
	}
	if fromRequest != "" && fromRequest != t[0] {
		return "", "", status.Error(codes.InvalidArgument, "tenant_id does not match the credentials")
	}
	return t[0], a[0], nil
}

func decodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

// toStatus maps domain errors to gRPC codes; anything unexpected is hidden behind a plain Internal.
func toStatus(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrUnknownType):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, ErrUnitBusy), errors.Is(err, ErrLocked):
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	return status.Error(codes.Internal, "internal error")
}

func ackPayload(e hashchain.Entry) []byte {
	b, _ := json.Marshal(map[string]any{"sequence_id": e.SequenceID, "entry_hash": e.EntryHash})
	return b
}

type incidentInput struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	Severity   string  `json:"severity"`
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	H3Cell     uint64  `json:"h3_cell"`
	ReportedAt int64   `json:"reported_at"`
}

func (s *Server) ReportIncident(ctx context.Context, in *pb.IncidentReport) (*pb.IncidentAck, error) {
	tenant, actor, err := identity(ctx, in.GetTenantId())
	if err != nil {
		return nil, err
	}
	var req incidentInput
	if err := decodeStrict(in.GetPayload(), &req); err != nil {
		return nil, toStatus(err)
	}
	e, err := s.d.ReportIncident(ctx, tenant, actor, IncidentView{ID: req.ID, Kind: req.Kind, Severity: req.Severity, Lat: req.Lat, Lon: req.Lon, H3Cell: req.H3Cell, ReportedAt: req.ReportedAt})
	if err != nil {
		return nil, toStatus(err)
	}
	s.ob.Notify(tenant)
	return &pb.IncidentAck{TenantId: tenant, Payload: ackPayload(e)}, nil
}

func (s *Server) DispatchUnit(ctx context.Context, in *pb.DispatchRequest) (*pb.DispatchAck, error) {
	tenant, actor, err := identity(ctx, in.GetTenantId())
	if err != nil {
		return nil, err
	}
	var req struct {
		IncidentID string `json:"incident_id"`
		UnitID     string `json:"unit_id"`
	}
	if err := decodeStrict(in.GetPayload(), &req); err != nil {
		return nil, toStatus(err)
	}
	e, err := s.d.DispatchUnit(ctx, tenant, actor, req.IncidentID, req.UnitID)
	if err != nil {
		return nil, toStatus(err)
	}
	s.ob.Notify(tenant)
	return &pb.DispatchAck{TenantId: tenant, Payload: ackPayload(e)}, nil
}

func (s *Server) AddHazard(ctx context.Context, in *pb.HazardPolygon) (*pb.HazardAck, error) {
	tenant, actor, err := identity(ctx, in.GetTenantId())
	if err != nil {
		return nil, err
	}
	var req struct {
		ID      string       `json:"id"`
		Kind    string       `json:"kind"`
		Polygon [][2]float64 `json:"polygon"`
	}
	if err := decodeStrict(in.GetPayload(), &req); err != nil {
		return nil, toStatus(err)
	}
	e, err := s.d.AddHazard(ctx, tenant, actor, HazardView{ID: req.ID, Kind: req.Kind, Polygon: req.Polygon})
	if err != nil {
		return nil, toStatus(err)
	}
	s.ob.Notify(tenant)
	return &pb.HazardAck{TenantId: tenant, Payload: ackPayload(e)}, nil
}

func (s *Server) VerifyChain(ctx context.Context, in *pb.VerifyRequest) (*pb.VerifyReply, error) {
	tenant, _, err := identity(ctx, in.GetTenantId())
	if err != nil {
		return nil, err
	}
	r, err := s.d.VerifyChain(ctx, tenant)
	if err != nil {
		return nil, toStatus(err)
	}
	b, _ := json.Marshal(r)
	return &pb.VerifyReply{TenantId: tenant, Payload: b}, nil
}
