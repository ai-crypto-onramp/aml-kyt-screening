// Package main is the kyt service entrypoint. It wires the store (Postgres +
// cache), vendor providers, alert service, audit emitter, and webhook processor
// into the HTTP server and runs migrations on startup.
//
// When DB_URL is unset the service boots in a degraded in-memory mode suitable
// for local development without external dependencies; production deployments
// must set DB_URL.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/alert"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/api"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/audit"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/decision"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/grpcserver"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/review"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/screen"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/store"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/tracing"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/vendor"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/webhook"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatalf("kyt: %v", err)
	}
}

// run wires all services and runs the HTTP + gRPC servers until ctx is
// cancelled.
func run(ctx context.Context) error {
	if _, err := tracing.Install(ctx); err != nil {
		return fmt.Errorf("install tracer: %w", err)
	}
	defer tracing.Shutdown(context.Background())

	cfg := store.LoadConfig()

	services, cleanup, err := buildServices(ctx, cfg)
	if err != nil {
		return err
	}
	if services.Audit != nil {
		defer services.Audit.Close()
	}
	if cleanup != nil {
		defer cleanup()
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	srv := api.NewServer(services, ":"+port)

	grpcPort := os.Getenv("GRPC_PORT")
	if grpcPort == "" {
		grpcPort = "9090"
	}
	grpcSrv := grpcserver.NewServer(services)

	errCh := make(chan error, 2)
	go func() {
		log.Printf("kyt REST listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("rest listen: %w", err)
		}
	}()
	go func() {
		log.Printf("kyt gRPC listening on :%s", grpcPort)
		if err := grpcSrv.Start(":" + grpcPort); err != nil {
			errCh <- fmt.Errorf("grpc serve: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		log.Println("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		grpcSrv.Stop()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		grpcSrv.Stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return err
	}
}

// buildServices constructs the *api.Services from the given config. When
// DB_URL is unset it falls back to an in-memory cache + screen store so the
// service still boots for local development. The returned cleanup function
// releases DB/cache resources and must be called by the caller when done.
func buildServices(ctx context.Context, cfg store.Config) (*api.Services, func(), error) {
	cache, db, cleanup, err := openCache(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}

	breaker := vendor.NewCircuitBreaker(cfg.CircuitBreakerThreshold, 60*time.Second)
	provider := buildProvider(cfg, breaker)
	if provider != nil {
		provider = vendor.NewIdempotentProvider(provider, vendor.NewMemoryResponseStore())
	}
	if provider == nil {
		provider = vendor.NewMockProvider("mock")
	}

	thresholds := decision.DefaultThresholds()
	var screenStore screen.ScreenStore
	if db != nil {
		screenStore = store.NewPGScreenStore(db)
	} else {
		screenStore = screen.NewMemoryScreenStore()
	}
	var alertStore alert.Store
	if db != nil {
		alertStore = store.NewPGAlertStore(db)
	} else {
		alertStore = alert.NewMemoryStore()
	}
	alerts := alert.NewService(alertStore)
	auditSink, auditCleanup, err := buildAuditSink(ctx, db)
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		return nil, nil, fmt.Errorf("build audit sink: %w", err)
	}
	if auditCleanup != nil {
		prev := cleanup
		cleanup = func() {
			if prev != nil {
				prev()
			}
			auditCleanup()
		}
	}
	auditEmitter := audit.NewEmitter(auditSink, 1024)
	screenSvc := screen.NewService(cache, provider, thresholds, screenStore, alerts, auditEmitter)

	verifier := webhook.NewVerifier(map[string][]byte{
		"chainalysis": []byte(os.Getenv("CHAINALYSIS_WEBHOOK_SECRET")),
		"trm":         []byte(os.Getenv("TRM_WEBHOOK_SECRET")),
	})
	proc := webhook.NewProcessor(verifier, cache, alerts).WithReviewer(review.NewTrigger(screenStore, alerts))

	return &api.Services{Screen: screenSvc, Alerts: alerts, Webhook: proc, Audit: auditEmitter}, cleanup, nil
}

// buildAuditSink selects the audit Sink based on AUDIT_EVENT_BUS_URL:
//   - unset -> DBSink when DB_URL is set, otherwise MemorySink (DB-less mode)
//   - nats:// or tls:// -> NATSSink
//   - memory:// or empty -> MemorySink
func buildAuditSink(ctx context.Context, db *sql.DB) (audit.Sink, func(), error) {
	busURL := os.Getenv("AUDIT_EVENT_BUS_URL")
	switch {
	case strings.HasPrefix(busURL, "nats://"), strings.HasPrefix(busURL, "tls://"):
		sink, err := audit.NewNATSSink(busURL, audit.DefaultAuditSubject)
		if err != nil {
			return nil, nil, err
		}
		return sink, func() { _ = sink.Close() }, nil
	case strings.HasPrefix(busURL, "memory://"):
		return audit.NewMemorySink(), nil, nil
	case busURL == "":
		if db != nil {
			return audit.NewDBSink(db), nil, nil
		}
		return audit.NewMemorySink(), nil, nil
	default:
		return nil, nil, fmt.Errorf("audit: unknown event bus scheme in %q", busURL)
	}
}

// openCache opens the DB and cache. When DB_URL is unset it returns an
// in-memory cache and a nil db.
func openCache(ctx context.Context, cfg store.Config) (screen.Cache, *sql.DB, func(), error) {
	if cfg.DBURL == "" {
		return screen.NewMemoryCache(cfg.CacheTTL, cfg.SanctionedCacheTTL), nil, nil, nil
	}
	db, err := store.Open(ctx, cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	cache, err := store.NewCache(ctx, cfg, db)
	if err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	cleanup := func() {
		_ = cache.Close()
		_ = db.Close()
	}
	return &storeCacheAdapter{cache: cache}, db, cleanup, nil
}

// storeCacheAdapter adapts store.Cache to screen.Cache.
type storeCacheAdapter struct {
	cache store.Cache
}

func (a *storeCacheAdapter) Get(ctx context.Context, address, chain string) (screen.Verdict, bool, error) {
	v, ok, err := a.cache.Get(ctx, address, chain)
	if err != nil {
		return screen.Verdict{}, false, err
	}
	if !ok {
		return screen.Verdict{}, false, nil
	}
	return screen.Verdict{
		Address:   v.Address,
		Chain:     v.Chain,
		RiskScore: v.RiskScore,
		Exposure:  v.Exposure,
		Decision:  v.Decision,
		Vendor:    v.Vendor,
	}, true, nil
}

func (a *storeCacheAdapter) Set(ctx context.Context, v screen.Verdict) error {
	return a.cache.Set(ctx, store.Verdict{
		Address:   v.Address,
		Chain:     v.Chain,
		RiskScore: v.RiskScore,
		Exposure:  v.Exposure,
		Decision:  v.Decision,
		Vendor:    v.Vendor,
	})
}

func (a *storeCacheAdapter) Delete(ctx context.Context, address, chain string) error {
	return a.cache.Delete(ctx, address, chain)
}

// buildProvider returns the configured ScreenProvider or nil if no vendor is
// configured.
func buildProvider(cfg store.Config, breaker *vendor.CircuitBreaker) vendor.ScreenProvider {
	vcfg := vendor.DefaultConfig()
	if cfg.VendorTimeout > 0 {
		vcfg.VendorTimeout = cfg.VendorTimeout
	}
	if cfg.CircuitBreakerThreshold > 0 {
		vcfg.CircuitBreakerThreshold = cfg.CircuitBreakerThreshold
	}
	switch {
	case vcfg.ChainalysisAPIKey != "":
		return vendor.NewHTTPProvider("chainalysis", vcfg.ChainalysisAPIKey, vcfg.ChainalysisAPIURL, vcfg.VendorTimeout, vendor.ChainalysisEncoder, vendor.ChainalysisDecoder, breaker)
	case vcfg.TRMAPIKey != "":
		return vendor.NewHTTPProvider("trm", vcfg.TRMAPIKey, vcfg.TRMAPIURL, vcfg.VendorTimeout, vendor.TRMEncoder, vendor.TRMDecoder, breaker)
	}
	return nil
}