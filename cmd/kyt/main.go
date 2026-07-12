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
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/alert"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/api"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/audit"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/decision"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/screen"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/store"
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

// run wires all services and runs the HTTP server until ctx is cancelled.
func run(ctx context.Context) error {
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

	go func() {
		log.Printf("kyt listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// buildServices constructs the *api.Services from the given config. When
// DB_URL is unset it falls back to an in-memory cache + screen store so the
// service still boots for local development. The returned cleanup function
// releases DB/cache resources and must be called by the caller when done.
func buildServices(ctx context.Context, cfg store.Config) (*api.Services, func(), error) {
	cache, cleanup, err := openCache(ctx, cfg)
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
	screenStore := screen.NewMemoryScreenStore()
	alerts := alert.NewService(alert.NewMemoryStore())
	auditEmitter := audit.NewEmitter(audit.NewMemorySink(), 1024)
	screenSvc := screen.NewService(cache, provider, thresholds, screenStore, alerts, auditEmitter)

	verifier := webhook.NewVerifier(map[string][]byte{
		"chainalysis": []byte(os.Getenv("CHAINALYSIS_WEBHOOK_SECRET")),
		"trm":         []byte(os.Getenv("TRM_WEBHOOK_SECRET")),
	})
	proc := webhook.NewProcessor(verifier, cache, alerts)

	return &api.Services{Screen: screenSvc, Alerts: alerts, Webhook: proc, Audit: auditEmitter}, cleanup, nil
}

// openCache opens the DB and cache. When DB_URL is unset it returns an
// in-memory cache and a nil db.
func openCache(ctx context.Context, cfg store.Config) (screen.Cache, func(), error) {
	if cfg.DBURL == "" {
		return screen.NewMemoryCache(cfg.CacheTTL, cfg.SanctionedCacheTTL), nil, nil
	}
	db, err := store.Open(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	cache, err := store.NewCache(ctx, cfg, db)
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	cleanup := func() {
		_ = cache.Close()
		_ = db.Close()
	}
	return &storeCacheAdapter{cache: cache}, cleanup, nil
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