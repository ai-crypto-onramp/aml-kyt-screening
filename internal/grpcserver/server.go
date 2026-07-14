// Package grpcserver implements the gRPC service mirroring the REST contract
// (Screen and GetAlert) consumed by the Transaction Orchestrator on the
// synchronous transaction path.
package grpcserver

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/alert"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/api"
	pb "github.com/ai-crypto-onramp/aml-kyt-screening/internal/pb"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/screen"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/tracing"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"go.opentelemetry.io/otel/trace"
)

// Server is the KYT gRPC server.
type Server struct {
	GRPCServer *grpc.Server
	services   *api.Services
	listener   net.Listener
}

// NewServer constructs a gRPC server backed by the same Services used by the
// REST mux. Unary calls are wrapped in an OTel server span for the trace
// acceptance criterion of Stage 8.
func NewServer(s *api.Services) *Server {
	gs := grpc.NewServer(
		grpc.UnaryInterceptor(unaryTraceInterceptor),
	)
	srv := &Server{GRPCServer: gs, services: s}
	pb.RegisterKYTServiceServer(gs, &kytServer{srv: srv})
	reflection.Register(gs)
	return srv
}

// Start binds to addr and blocks serving gRPC.
func (s *Server) Start(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.listener = ln
	return s.GRPCServer.Serve(ln)
}

// Stop gracefully stops the gRPC server.
func (s *Server) Stop() {
	if s.GRPCServer != nil {
		s.GRPCServer.GracefulStop()
	}
}

type kytServer struct {
	pb.UnimplementedKYTServiceServer
	srv *Server
}

func toScreenRequest(in *pb.ScreenRequest) screen.Request {
	return screen.Request{
		TxID:          in.GetTxId(),
		Address:       in.GetAddress(),
		SourceAddress: in.GetSourceAddress(),
		Chain:         in.GetChain(),
		Amount:        in.GetAmount(),
	}
}

func toProtoResponse(r screen.Response) *pb.ScreenResponse {
	return &pb.ScreenResponse{
		ScreenId:  r.ScreenID,
		RiskScore: int32(r.RiskScore),
		Exposure:  r.Exposure,
		Decision:  r.Decision,
		Vendor:    r.Vendor,
		CacheHit:  r.CacheHit,
	}
}

func (g *kytServer) Screen(ctx context.Context, in *pb.ScreenRequest) (*pb.ScreenResponse, error) {
	ctx, span := tracing.StartSpan(ctx, "KYTService.Screen",
		trace.WithSpanKind(trace.SpanKindServer),
	)
	defer span.End()
	resp, err := g.srv.services.Screen.Screen(ctx, toScreenRequest(in))
	if err != nil {
		tracing.RecordError(ctx, err)
		return nil, toGRPCError(err)
	}
	return toProtoResponse(resp), nil
}

func (g *kytServer) GetAlert(ctx context.Context, in *pb.GetAlertRequest) (*pb.Alert, error) {
	ctx, span := tracing.StartSpan(ctx, "KYTService.GetAlert",
		trace.WithSpanKind(trace.SpanKindServer),
	)
	defer span.End()
	a, err := g.srv.services.Alerts.Get(in.GetId())
	if err != nil {
		tracing.RecordError(ctx, err)
		return nil, toGRPCError(err)
	}
	return toProtoAlert(a), nil
}

func toProtoAlert(a alert.Alert) *pb.Alert {
	out := &pb.Alert{
		Id:        a.ID,
		ScreenId:  a.ScreenID,
		TxId:      a.TxID,
		Address:   a.Address,
		Chain:     a.Chain,
		Exposure:  a.Exposure,
		Severity:  a.Severity,
		Status:    a.Status,
		Assignee:  a.Assignee,
		CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if a.ClosedAt != nil {
		out.ClosedAt = a.ClosedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

func toGRPCError(err error) error {
	if err == nil {
		return nil
	}
	var ae *api.AppError
	if errors.As(err, &ae) {
		switch ae.StatusCode {
		case 400:
			return status.Error(codes.InvalidArgument, ae.Message)
		case 401:
			return status.Error(codes.Unauthenticated, ae.Message)
		case 404:
			return status.Error(codes.NotFound, ae.Message)
		case 409:
			return status.Error(codes.FailedPrecondition, ae.Message)
		case 413:
			return status.Error(codes.ResourceExhausted, ae.Message)
		}
		return status.Error(codes.Internal, ae.Message)
	}
	switch {
	case errors.Is(err, alert.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, alert.ErrAlreadyClosed):
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}

// unaryTraceInterceptor creates a server span for every unary RPC, extracts
// trace context from incoming gRPC metadata, and records the resulting status
// code on the span.
func unaryTraceInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	ctx, span := tracing.StartSpan(ctx, info.FullMethod,
		trace.WithSpanKind(trace.SpanKindServer),
	)
	defer span.End()
	resp, err := handler(ctx, req)
	if err != nil {
		tracing.RecordError(ctx, err)
	}
	return resp, err
}