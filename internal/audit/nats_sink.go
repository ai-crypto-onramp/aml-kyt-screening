package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
)

// DefaultAuditSubject is the NATS subject audit events are published to when
// AUDIT_EVENT_BUS_URL is set and no subject is configured via
// AUDIT_EVENT_BUS_SUBJECT.
const DefaultAuditSubject = "kyt.audit.v1"

// NewSink selects an audit Sink based on url:
//   - "" or "memory://" -> MemorySink (DB-less / testing fallback)
//   - "nats://" or "tls://" -> NATSSink
// Any other scheme returns an error.
func NewSink(url string) (Sink, error) {
	switch {
	case url == "" || strings.HasPrefix(url, "memory://"):
		return NewMemorySink(), nil
	case strings.HasPrefix(url, "nats://") || strings.HasPrefix(url, "tls://"):
		return NewNATSSink(url, DefaultAuditSubject)
	default:
		return nil, fmt.Errorf("audit: unknown event bus scheme in %q", url)
	}
}

// NATSSink publishes audit events to a NATS subject. It is used when
// AUDIT_EVENT_BUS_URL is set; otherwise the Emitter falls back to the
// in-memory / DB sink.
type NATSSink struct {
	conn    *nats.Conn
	subject string
	sent    atomic.Int64
}

// NewNATSSink connects to the NATS cluster at url and returns a NATSSink that
// publishes JSON-encoded events to subject.
func NewNATSSink(url, subject string) (*NATSSink, error) {
	if subject == "" {
		subject = DefaultAuditSubject
	}
	nc, err := nats.Connect(url, nats.Timeout(5*time.Second), nats.MaxReconnects(-1))
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	return &NATSSink{conn: nc, subject: subject}, nil
}

// Emit JSON-encodes e and publishes it to the configured NATS subject.
func (s *NATSSink) Emit(ctx context.Context, e Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("nats encode: %w", err)
	}
	if err := s.conn.Publish(s.subject, body); err != nil {
		return fmt.Errorf("nats publish: %w", err)
	}
	s.sent.Add(1)
	return nil
}

// Close drains and closes the NATS connection.
func (s *NATSSink) Close() error {
	if s.conn == nil {
		return nil
	}
	s.conn.Drain()
	s.conn.Close()
	return nil
}

// Sent returns the number of events successfully published.
func (s *NATSSink) Sent() int64 { return s.sent.Load() }