package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/alert"
)

func sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifierValidSignature(t *testing.T) {
	v := NewVerifier(map[string][]byte{"chainalysis": []byte("secret")})
	body := []byte(`{"address":"0x1"}`)
	if err := v.Verify("chainalysis", body, sign([]byte("secret"), body)); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestVerifierTampered(t *testing.T) {
	v := NewVerifier(map[string][]byte{"chainalysis": []byte("secret")})
	body := []byte(`{"address":"0x1"}`)
	if err := v.Verify("chainalysis", body, "deadbeef"); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("err: %v", err)
	}
}

func TestVerifierUnknownVendor(t *testing.T) {
	v := NewVerifier(map[string][]byte{"chainalysis": []byte("secret")})
	if err := v.Verify("trm", []byte("x"), "sig"); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("err: %v", err)
	}
}

func TestVerifierEmptySig(t *testing.T) {
	v := NewVerifier(map[string][]byte{"chainalysis": []byte("secret")})
	if err := v.Verify("chainalysis", []byte("x"), ""); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("err: %v", err)
	}
}

func TestVerifierInvalidHex(t *testing.T) {
	v := NewVerifier(map[string][]byte{"chainalysis": []byte("secret")})
	if err := v.Verify("chainalysis", []byte("x"), "nothex"); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("err: %v", err)
	}
}

type mockCache struct {
	deleted map[string]bool
	err     error
}

func (m *mockCache) Delete(_ context.Context, address, chain string) error {
	if m.err != nil {
		return m.err
	}
	if m.deleted == nil {
		m.deleted = make(map[string]bool)
	}
	m.deleted[address+"|"+chain] = true
	return nil
}

func TestProcessorIngestValid(t *testing.T) {
	v := NewVerifier(map[string][]byte{"chainalysis": []byte("secret")})
	cache := &mockCache{}
	alerts := alert.NewService(alert.NewMemoryStore())
	p := NewProcessor(v, cache, alerts)

	body := []byte(`{"event_id":"e1","address":"0x1","chain":"ethereum","exposure":"SANCTIONED","tx_id":"tx1"}`)
	res := p.Ingest(context.Background(), "chainalysis", body, sign([]byte("secret"), body))
	if !res.Accepted {
		t.Fatalf("expected accepted, got %+v", res)
	}
	if !cache.deleted["0x1|ethereum"] {
		t.Error("cache not invalidated")
	}
	open, _ := alerts.List(alert.StatusOpen)
	if len(open) != 1 || open[0].Exposure != "SANCTIONED" || open[0].Severity != "critical" {
		t.Fatalf("alerts: %+v", open)
	}
}

func TestProcessorIngestTampered(t *testing.T) {
	v := NewVerifier(map[string][]byte{"chainalysis": []byte("secret")})
	cache := &mockCache{}
	alerts := alert.NewService(alert.NewMemoryStore())
	p := NewProcessor(v, cache, alerts)
	body := []byte(`{"event_id":"e1","address":"0x1","chain":"ethereum","exposure":"SANCTIONED"}`)
	res := p.Ingest(context.Background(), "chainalysis", body, "bad-sig")
	if res.Accepted {
		t.Fatal("tampered payload must not be accepted")
	}
	if cache.deleted != nil {
		t.Fatal("cache must not be invalidated on tampered payload")
	}
	open, _ := alerts.List(alert.StatusOpen)
	if len(open) != 0 {
		t.Fatal("no alert should be created on tampered payload")
	}
}

func TestProcessorIngestDuplicate(t *testing.T) {
	v := NewVerifier(map[string][]byte{"chainalysis": []byte("secret")})
	cache := &mockCache{}
	alerts := alert.NewService(alert.NewMemoryStore())
	p := NewProcessor(v, cache, alerts)

	body := []byte(`{"event_id":"e1","address":"0x1","chain":"ethereum","exposure":"SANCTIONED"}`)
	sig := sign([]byte("secret"), body)
	if res := p.Ingest(context.Background(), "chainalysis", body, sig); !res.Accepted {
		t.Fatalf("first ingest: %+v", res)
	}
	res := p.Ingest(context.Background(), "chainalysis", body, sig)
	if !res.Duplicate {
		t.Fatalf("expected duplicate, got %+v", res)
	}
	open, _ := alerts.List(alert.StatusOpen)
	if len(open) != 1 {
		t.Fatalf("expected 1 alert after duplicate, got %d", len(open))
	}
}

func TestProcessorIngestMissingFields(t *testing.T) {
	v := NewVerifier(map[string][]byte{"chainalysis": []byte("secret")})
	p := NewProcessor(v, &mockCache{}, alert.NewService(alert.NewMemoryStore()))
	body := []byte(`{"event_id":"e1"}`)
	res := p.Ingest(context.Background(), "chainalysis", body, sign([]byte("secret"), body))
	if res.Accepted || res.Duplicate {
		t.Fatalf("expected rejected, got %+v", res)
	}
}

func TestProcessorIngestInvalidJSON(t *testing.T) {
	v := NewVerifier(map[string][]byte{"chainalysis": []byte("secret")})
	p := NewProcessor(v, &mockCache{}, alert.NewService(alert.NewMemoryStore()))
	body := []byte(`not-json`)
	res := p.Ingest(context.Background(), "chainalysis", body, sign([]byte("secret"), body))
	if res.Accepted {
		t.Fatal("expected rejection for invalid json")
	}
}

func TestProcessorIngestCacheError(t *testing.T) {
	v := NewVerifier(map[string][]byte{"chainalysis": []byte("secret")})
	cache := &mockCache{err: errors.New("cache down")}
	p := NewProcessor(v, cache, alert.NewService(alert.NewMemoryStore()))
	body := []byte(`{"event_id":"e1","address":"0x1","chain":"ethereum","exposure":"SANCTIONED"}`)
	res := p.Ingest(context.Background(), "chainalysis", body, sign([]byte("secret"), body))
	if res.Accepted {
		t.Fatal("expected rejection on cache error")
	}
	if res.Reason == "" {
		t.Error("expected reason")
	}
}

func TestProcessorIngestSynthesizesEventID(t *testing.T) {
	v := NewVerifier(map[string][]byte{"chainalysis": []byte("secret")})
	p := NewProcessor(v, &mockCache{}, alert.NewService(alert.NewMemoryStore()))
	body := []byte(`{"address":"0x1","chain":"ethereum","exposure":"SANCTIONED"}`)
	res := p.Ingest(context.Background(), "chainalysis", body, sign([]byte("secret"), body))
	if !res.Accepted {
		t.Fatalf("expected accepted, got %+v", res)
	}
	if res.EventID == "" {
		t.Error("expected synthesized event id")
	}
}

func TestReadBody(t *testing.T) {
	body, err := ReadBody(bytes.NewBufferString("hello"), 1024)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != "hello" {
		t.Fatalf("body: %q", body)
	}
	if _, err := ReadBody(bytes.NewBuffer(make([]byte, 2048)), 1024); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("err: %v", err)
	}
}

type mockReviewer struct {
	mu     sync.Mutex
	called bool
	addr   string
	chain  string
	exp    string
	txID   string
}

func (m *mockReviewer) TriggerReview(_ context.Context, address, chain, exposure, txID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.called = true
	m.addr = address
	m.chain = chain
	m.exp = exposure
	m.txID = txID
	return nil
}

func (m *mockReviewer) snapshot() (bool, string, string, string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.called, m.addr, m.chain, m.exp, m.txID
}

func TestProcessorWithReviewerTriggersOnAccept(t *testing.T) {
	v := NewVerifier(map[string][]byte{"chainalysis": []byte("secret")})
	cache := &mockCache{}
	alerts := alert.NewService(alert.NewMemoryStore())
	rv := &mockReviewer{}
	p := NewProcessor(v, cache, alerts).WithReviewer(rv)

	body := []byte(`{"event_id":"e1","address":"0x1","chain":"ethereum","exposure":"SANCTIONED","tx_id":"tx1"}`)
	res := p.Ingest(context.Background(), "chainalysis", body, sign([]byte("secret"), body))
	if !res.Accepted {
		t.Fatalf("expected accepted, got %+v", res)
	}
	// The reviewer runs asynchronously; poll briefly until invoked.
	deadline := time.Now().Add(time.Second)
	for {
		called, addr, chain, exp, txID := rv.snapshot()
		if called {
			if addr != "0x1" || chain != "ethereum" || exp != "SANCTIONED" || txID != "tx1" {
				t.Errorf("reviewer args: addr=%s chain=%s exp=%s txID=%s", addr, chain, exp, txID)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reviewer not invoked")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestProcessorWithReviewerNotTriggeredOnTamper(t *testing.T) {
	v := NewVerifier(map[string][]byte{"chainalysis": []byte("secret")})
	alerts := alert.NewService(alert.NewMemoryStore())
	rv := &mockReviewer{}
	p := NewProcessor(v, &mockCache{}, alerts).WithReviewer(rv)
	body := []byte(`{"event_id":"e1","address":"0x1","chain":"ethereum","exposure":"SANCTIONED"}`)
	_ = p.Ingest(context.Background(), "chainalysis", body, "bad-sig")
	called, _, _, _, _ := rv.snapshot()
	if called {
		t.Fatal("reviewer must not be invoked on tampered payload")
	}
}