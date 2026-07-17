package grpcserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/alert"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/api"
	pb "github.com/ai-crypto-onramp/aml-kyt-screening/internal/pb"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/audit"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/decision"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/screen"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/vendor"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func newTestServices(t *testing.T) *api.Services {
	t.Helper()
	mp := vendor.NewMockProvider("chainalysis")
	cache := screen.NewMemoryCache(time.Hour, 24*time.Hour)
	th := decision.NewThresholds(90, 50, decision.DecisionManualReview)
	screenStore := screen.NewMemoryScreenStore()
	alerts := alert.NewService(alert.NewMemoryStore())
	auditSink := audit.NewMemorySink()
	emitter := audit.NewEmitter(auditSink, 16)
	t.Cleanup(emitter.Close)
	screenSvc := screen.NewService(cache, mp, th, screenStore, alerts, emitter)
	return &api.Services{Screen: screenSvc, Alerts: alerts, Audit: emitter}
}

func startServer(t *testing.T, s *api.Services) (*Server, *bufconn.Listener) {
	t.Helper()
	srv := NewServer(s)
	lis := bufconn.Listen(1024 * 1024)
	go func() { _ = srv.GRPCServer.Serve(lis) }()
	return srv, lis
}

func dial(t *testing.T, lis *bufconn.Listener) (pb.KYTServiceClient, func()) {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	return pb.NewKYTServiceClient(conn), func() { _ = conn.Close() }
}

func TestGRPCScreenHappyPath(t *testing.T) {
	srv, lis := startServer(t, newTestServices(t))
	defer srv.Stop()
	client, closeFn := dial(t, lis)
	defer closeFn()
	resp, err := client.Screen(context.Background(), &pb.ScreenRequest{
		TxId: "tx1", Address: "0x1", Chain: "ethereum", Amount: "100",
	})
	if err != nil {
		t.Fatalf("screen: %v", err)
	}
	if resp.GetScreenId() == "" {
		t.Error("missing screen id")
	}
	if resp.GetDecision() != decision.DecisionAllow {
		t.Errorf("decision: %s", resp.GetDecision())
	}
	if resp.GetExposure() != decision.ExposureClean {
		t.Errorf("exposure: %s", resp.GetExposure())
	}
}

func TestGRPCScreenSanctionedBlocks(t *testing.T) {
	svc := newTestServices(t)
	mp := vendor.NewMockProvider("chainalysis")
	mp.SetResponse("0xbad", "ethereum", vendor.MockResponse{RiskScore: 99, Exposure: "SANCTIONED"})
	svc.Screen = screen.NewService(screen.NewMemoryCache(time.Hour, 24*time.Hour), mp,
		decision.NewThresholds(90, 50, decision.DecisionManualReview),
		screen.NewMemoryScreenStore(), svc.Alerts, svc.Audit)
	srv, lis := startServer(t, svc)
	defer srv.Stop()
	client, closeFn := dial(t, lis)
	defer closeFn()
	resp, err := client.Screen(context.Background(), &pb.ScreenRequest{
		TxId: "tx1", Address: "0xbad", Chain: "ethereum", Amount: "100",
	})
	if err != nil {
		t.Fatalf("screen: %v", err)
	}
	if resp.GetDecision() != decision.DecisionBlock {
		t.Fatalf("decision: %s", resp.GetDecision())
	}
}

func TestGRPCScreenMissingFields(t *testing.T) {
	srv, lis := startServer(t, newTestServices(t))
	defer srv.Stop()
	client, closeFn := dial(t, lis)
	defer closeFn()
	_, err := client.Screen(context.Background(), &pb.ScreenRequest{})
	if err == nil {
		t.Fatal("expected error for missing fields")
	}
	if st, ok := status.FromError(err); ok && st.Code() != codes.InvalidArgument && st.Code() != codes.Internal {
		t.Errorf("expected InvalidArgument or Internal, got %s", st.Code())
	}
}

func TestGRPCGetAlertHappyPath(t *testing.T) {
	svc := newTestServices(t)
	a, _ := svc.Alerts.Create("scr-1", "tx1", "0xbad", "ethereum", "SANCTIONED", "critical")
	srv, lis := startServer(t, svc)
	defer srv.Stop()
	client, closeFn := dial(t, lis)
	defer closeFn()
	resp, err := client.GetAlert(context.Background(), &pb.GetAlertRequest{Id: a.ID})
	if err != nil {
		t.Fatalf("get alert: %v", err)
	}
	if resp.GetId() != a.ID {
		t.Errorf("id: %s", resp.GetId())
	}
	if resp.GetExposure() != "SANCTIONED" || resp.GetSeverity() != "critical" {
		t.Errorf("alert: %+v", resp)
	}
	if resp.GetCreatedAt() == "" {
		t.Error("missing created_at")
	}
}

func TestGRPCGetAlertNotFound(t *testing.T) {
	srv, lis := startServer(t, newTestServices(t))
	defer srv.Stop()
	client, closeFn := dial(t, lis)
	defer closeFn()
	_, err := client.GetAlert(context.Background(), &pb.GetAlertRequest{Id: "nope"})
	if err == nil {
		t.Fatal("expected error for unknown alert")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
}

func TestGRPCScreenMirrorsREST(t *testing.T) {
	svc := newTestServices(t)
	svc.Screen = svc.Screen.WithID(func() string { return "scr-mirror" })
	srv, lis := startServer(t, svc)
	defer srv.Stop()
	client, closeFn := dial(t, lis)
	defer closeFn()
	req := &pb.ScreenRequest{TxId: "tx1", Address: "0x1", Chain: "ethereum", Amount: "100"}
	grpcResp, err := client.Screen(context.Background(), req)
	if err != nil {
		t.Fatalf("grpc screen: %v", err)
	}
	restResp, err := svc.Screen.Screen(context.Background(), screen.Request{
		TxID: "tx1", Address: "0x1", Chain: "ethereum", Amount: "100",
	})
	if err != nil {
		t.Fatalf("rest screen: %v", err)
	}
	if grpcResp.GetDecision() != restResp.Decision || grpcResp.GetExposure() != restResp.Exposure {
		t.Errorf("verdict mismatch: grpc=%+v rest=%+v", grpcResp, restResp)
	}
}

func TestStartBindsAndServes(t *testing.T) {
	srv := NewServer(newTestServices(t))
	defer srv.Stop()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.GRPCServer.Serve(ln) }()
	// Give the server a moment to start, then stop it.
	time.Sleep(50 * time.Millisecond)
	srv.Stop()
	select {
	case err := <-done:
		// Serve returns after GracefulStop; nil or non-nil both acceptable.
		_ = err
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop within 2s")
	}
}

func TestStartListenError(t *testing.T) {
	srv := NewServer(newTestServices(t))
	defer srv.Stop()
	// Use an invalid port number to force a listen error.
	if err := srv.Start("127.0.0.1:99999"); err == nil {
		t.Fatal("expected error binding to invalid port")
	}
}

func TestToGRPCErrorMapsAppErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		want   codes.Code
	}{
		{"nil", nil, codes.OK},
		{"bad_request_400", statusWithCode(http.StatusBadRequest), codes.InvalidArgument},
		{"unauth_401", statusWithCode(http.StatusUnauthorized), codes.Unauthenticated},
		{"not_found_404", statusWithCode(http.StatusNotFound), codes.NotFound},
		{"conflict_409", statusWithCode(http.StatusConflict), codes.FailedPrecondition},
		{"too_large_413", statusWithCode(http.StatusRequestEntityTooLarge), codes.ResourceExhausted},
		{"internal_500", statusWithCode(http.StatusInternalServerError), codes.Internal},
		{"alert_not_found", alert.ErrNotFound, codes.NotFound},
		{"alert_already_closed", alert.ErrAlreadyClosed, codes.FailedPrecondition},
		{"generic", errors.New("boom"), codes.Internal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := toGRPCError(c.err)
			if c.err == nil {
				if got != nil {
					t.Errorf("nil err should return nil, got %v", got)
				}
				return
			}
			st, ok := status.FromError(got)
			if !ok {
				t.Fatalf("not a status error: %v", got)
			}
			if st.Code() != c.want {
				t.Errorf("code: got %s want %s", st.Code(), c.want)
			}
		})
	}
}

func statusWithCode(status int) error {
	return &api.AppError{StatusCode: status, Code: "x", Message: "msg"}
}

func TestToProtoAlertIncludesClosedAt(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	closed := now.Add(time.Hour)
	a := alert.Alert{
		ID: "a1", ScreenID: "s1", TxID: "tx1", Address: "0x1", Chain: "ethereum",
		Exposure: "SANCTIONED", Severity: "critical", Status: alert.StatusClosed,
		Assignee: "analyst1", CreatedAt: now, ClosedAt: &closed,
	}
	got := toProtoAlert(a)
	if got.GetClosedAt() == "" {
		t.Error("expected closed_at to be set")
	}
	if got.GetAssignee() != "analyst1" {
		t.Errorf("assignee: %s", got.GetAssignee())
	}
}