package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/segmentio/kafka-go"
)

// DefaultAuditTopic is the Kafka topic audit events are published to when
// AUDIT_EVENT_BUS_URL is set and no topic is configured via
// AUDIT_EVENT_BUS_TOPIC. The canonical audit topic is audit.v1 (consumed
// by audit-event-log); see .github/contracts/asyncapi/audit/v1/asyncapi.yaml.
const DefaultAuditTopic = "audit.v1"

// NewSink selects an audit Sink based on url:
//   - "" or "memory://" -> MemorySink (DB-less / testing fallback)
//   - "kafka://" or plain "host:9092[,host2]" -> KafkaSink
//
// Any other scheme returns an error.
func NewSink(url string) (Sink, error) {
	switch {
	case url == "" || strings.HasPrefix(url, "memory://"):
		return NewMemorySink(), nil
	case strings.HasPrefix(url, "kafka://"):
		return NewKafkaSinkFromURL(url, DefaultAuditTopic)
	case isPlainBrokerList(url):
		return NewKafkaSinkFromURL("kafka://"+url, DefaultAuditTopic)
	default:
		return nil, fmt.Errorf("audit: unknown event bus scheme in %q (use kafka://<brokers> or memory://)", url)
	}
}

// isPlainBrokerList reports whether url looks like a comma-separated list of
// host:port brokers with no scheme.
func isPlainBrokerList(url string) bool {
	if strings.Contains(url, "://") {
		return false
	}
	return strings.Contains(url, ":")
}

// KafkaSink publishes audit events to a Kafka topic. It is used when
// AUDIT_EVENT_BUS_URL is set; otherwise the Emitter falls back to the
// in-memory / DB sink.
type KafkaSink struct {
	writer *kafka.Writer
	topic  string
	sent   atomic.Int64
}

// NewKafkaSink returns a KafkaSink that publishes JSON-encoded events to
// topic on the given brokers. Events are keyed by screen_id so consumers
// receive per-screen ordering.
func NewKafkaSink(brokers []string, topic string) (*KafkaSink, error) {
	if len(brokers) == 0 {
		return nil, fmt.Errorf("audit kafka: no brokers provided")
	}
	if topic == "" {
		topic = DefaultAuditTopic
	}
	w := &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        topic,
		Balancer:     &kafka.LeastBytes{},
		BatchTimeout: 10 * time.Millisecond,
		RequiredAcks: kafka.RequireAll,
	}
	return &KafkaSink{writer: w, topic: topic}, nil
}

// NewKafkaSinkFromURL parses a "kafka://host:9092[,host2][?topic=t]" URL and
// returns a KafkaSink.
func NewKafkaSinkFromURL(url, defaultTopic string) (*KafkaSink, error) {
	rest := strings.TrimPrefix(url, "kafka://")
	topic := ""
	if i := strings.Index(rest, "?"); i >= 0 {
		q := rest[i+1:]
		rest = rest[:i]
		for _, kv := range strings.Split(q, "&") {
			if strings.HasPrefix(kv, "topic=") {
				topic = strings.TrimPrefix(kv, "topic=")
			}
		}
	}
	brokers := strings.Split(rest, ",")
	clean := brokers[:0]
	for _, b := range brokers {
		b = strings.TrimSpace(b)
		if b != "" {
			clean = append(clean, b)
		}
	}
	brokers = clean
	if topic == "" {
		topic = defaultTopic
	}
	return NewKafkaSink(brokers, topic)
}

// Emit JSON-encodes e into the canonical audit.v1 envelope and publishes it
// to the configured Kafka topic. See .github/contracts/asyncapi/audit/v1/asyncapi.yaml.
func (s *KafkaSink) Emit(ctx context.Context, e Event) error {
	if s.writer == nil {
		return fmt.Errorf("audit kafka: not connected")
	}
	payload, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("audit kafka encode: %w", err)
	}
	sum := sha256.Sum256(payload)
	payloadHash := "sha256:" + hex.EncodeToString(sum[:])
	id := uuid.NewString()
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	envelope := map[string]any{
		"schema_version":  "1",
		"id":               id,
		"ts":               e.CreatedAt.UTC().Format(time.RFC3339Nano),
		"source_service":   "aml-kyt-screening",
		"actor_id":         coalesce(e.Operator, "aml-kyt-screening"),
		"action":           "kyt.screen",
		"target_type":      "transaction",
		"target_id":        coalesce(e.TxID, e.ScreenID, e.Address),
		"payload_hash":     payloadHash,
		"payload":          json.RawMessage(payload),
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("audit kafka envelope encode: %w", err)
	}
	key := e.ScreenID
	if key == "" {
		key = e.TxID
	}
	if key == "" {
		key = id
	}
	if err := s.writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(key),
		Value: body,
	}); err != nil {
		return fmt.Errorf("audit kafka publish: %w", err)
	}
	s.sent.Add(1)
	return nil
}

func coalesce(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Close flushes and closes the underlying writer.
func (s *KafkaSink) Close() error {
	if s.writer == nil {
		return nil
	}
	return s.writer.Close()
}

// Sent returns the number of events successfully published.
func (s *KafkaSink) Sent() int64 { return s.sent.Load() }