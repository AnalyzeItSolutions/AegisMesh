package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	pb "github.com/aegismesh/aegismesh/gen/aegismeshv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// maxBody bounds one request: a delta batch of an offline device can be large, but not unbounded.
const maxBody = 8 << 20

// HTTPHandler is the JSON-over-HTTP front of the sync service, for the field app (a browser cannot speak plain gRPC). It calls the SAME PushDelta, PullDelta
// and StateHash the gRPC side does, so the two cannot drift apart. The request body and the reply body are the payload JSON of the contract. As on the gRPC
// side the tenant comes only from the x-tenant-id header the gateway sets after validating the token, never from the body.
//
//	POST /sync/push   body: a delta batch          -> {"accepted": n, "hash": "..."}
//	POST /sync/pull   body: {"since_seq": n}       -> a delta batch with "cursor"
//	GET  /sync/hash                                -> {"hash": "..."}
//	GET  /healthz
func (s *Server) HTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sync/push", s.httpRPC(func(ctx context.Context, body []byte) ([]byte, error) {
		out, err := s.PushDelta(ctx, &pb.DeltaBatch{Payload: body})
		return out.GetPayload(), err
	}))
	mux.HandleFunc("POST /sync/pull", s.httpRPC(func(ctx context.Context, body []byte) ([]byte, error) {
		out, err := s.PullDelta(ctx, &pb.PullRequest{Payload: body})
		return out.GetPayload(), err
	}))
	mux.HandleFunc("GET /sync/hash", s.httpRPC(func(ctx context.Context, _ []byte) ([]byte, error) {
		out, err := s.StateHash(ctx, &pb.HashRequest{})
		return out.GetPayload(), err
	}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}

func (s *Server) httpRPC(call func(ctx context.Context, body []byte) ([]byte, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
				return
			}
			writeError(w, http.StatusBadRequest, "could not read the request body")
			return
		}
		ctx := metadata.NewIncomingContext(r.Context(), metadata.Pairs(tenantHeader, r.Header.Get(tenantHeader)))
		out, err := call(ctx, body)
		if err != nil {
			writeError(w, httpStatus(status.Code(err)), status.Convert(err).Message())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	}
}

// httpStatus maps a gRPC status code to the HTTP status a client would expect.
func httpStatus(c codes.Code) int {
	switch c {
	case codes.OK:
		return http.StatusOK
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.InvalidArgument:
		return http.StatusBadRequest
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists, codes.FailedPrecondition:
		return http.StatusConflict
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func writeError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
