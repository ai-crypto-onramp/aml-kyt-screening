package audit

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewSinkMemoryByDefault(t *testing.T) {
	sink, err := NewSink("")
	if err != nil {
		t.Fatalf("NewSink: %v", err)
	}
	if _, ok := sink.(*MemorySink); !ok {
		t.Fatalf("expected *MemorySink, got %T", sink)
	}
}

func TestNewSinkMemoryScheme(t *testing.T) {
	sink, err := NewSink("memory://")
	if err != nil {
		t.Fatalf("NewSink: %v", err)
	}
	if _, ok := sink.(*MemorySink); !ok {
		t.Fatalf("expected *MemorySink, got %T", sink)
	}
}

func TestNewSinkUnknownScheme(t *testing.T) {
	_, err := NewSink("foobar://broker:9092")
	if err == nil || !strings.Contains(err.Error(), "unknown event bus scheme") {
		t.Fatalf("err: %v", err)
	}
}

func TestNewSinkRejectsNATSScheme(t *testing.T) {
	_, err := NewSink("nats://127.0.0.1:4222")
	if err == nil || !strings.Contains(err.Error(), "unknown event bus scheme") {
		t.Fatalf("expected unknown-scheme error for legacy nats:// url, got %v", err)
	}
}

func TestNewKafkaSinkFromURLNoBrokers(t *testing.T) {
	_, err := NewKafkaSinkFromURL("kafka://", DefaultAuditTopic)
	if err == nil || !strings.Contains(err.Error(), "no brokers") {
		t.Fatalf("expected no-brokers error, got %v", err)
	}
}

func TestNewKafkaSinkFromURLParsesTopic(t *testing.T) {
	// No real connection is made until Emit; just verify parsing.
	s, err := NewKafkaSinkFromURL("kafka://broker:9092?topic=custom.audit", DefaultAuditTopic)
	if err != nil {
		t.Fatalf("NewKafkaSinkFromURL: %v", err)
	}
	if s.topic != "custom.audit" {
		t.Fatalf("expected topic custom.audit, got %q", s.topic)
	}
	_ = s.Close()
}

func TestKafkaSinkEmitOnClosedWriter(t *testing.T) {
	sink := &KafkaSink{topic: "audit.v1"}
	if err := sink.Emit(context.Background(), Event{ScreenID: "s1"}); err == nil {
		t.Fatal("expected error emitting on nil writer")
	}
	if err := sink.Close(); err != nil {
		t.Errorf("close should be no-op on nil writer: %v", err)
	}
	if sink.Sent() != 0 {
		t.Errorf("sent should be 0, got %d", sink.Sent())
	}
}

type errSink struct{ err error }

func (s *errSink) Emit(_ context.Context, _ Event) error { return s.err }

func TestEmitterRecordsSinkFailuresFromCustomSink(t *testing.T) {
	sink := &errSink{err: errors.New("bus down")}
	emitter := NewEmitter(sink, 4)
	defer emitter.Close()
	_ = emitter.Emit(context.Background(), Event{})
	deadline := time.After(2 * time.Second)
	for emitter.Drops() == 0 {
		select {
		case <-deadline:
			t.Fatalf("no drop recorded; drops=%d", emitter.Drops())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestNewSinkPlainBrokerList(t *testing.T) {
	sink, err := NewSink("broker1:9092,broker2:9092")
	if err != nil {
		t.Fatalf("NewSink: %v", err)
	}
	ks, ok := sink.(*KafkaSink)
	if !ok {
		t.Fatalf("expected *KafkaSink, got %T", sink)
	}
	_ = ks.Close()
}

func TestIsPlainBrokerList(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"host:9092", true},
		{"host:9092,host2:9092", true},
		{"kafka://host:9092", false},
		{"http://host:9092", false},
		{"host", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isPlainBrokerList(c.in); got != c.want {
			t.Errorf("isPlainBrokerList(%q) = %v want %v", c.in, got, c.want)
		}
	}
}

func TestNewKafkaSinkFromURLTrimsWhitespace(t *testing.T) {
	s, err := NewKafkaSinkFromURL("kafka:// broker1:9092 ,  broker2:9092 ", DefaultAuditTopic)
	if err != nil {
		t.Fatalf("NewKafkaSinkFromURL: %v", err)
	}
	if s.topic != DefaultAuditTopic {
		t.Errorf("topic: %q", s.topic)
	}
	_ = s.Close()
}

func TestNewKafkaSinkNoBrokers(t *testing.T) {
	if _, err := NewKafkaSink(nil, ""); err == nil {
		t.Fatal("expected error for no brokers")
	}
}

func TestNewKafkaSinkEmptyTopicUsesDefault(t *testing.T) {
	s, err := NewKafkaSink([]string{"broker:9092"}, "")
	if err != nil {
		t.Fatalf("NewKafkaSink: %v", err)
	}
	if s.topic != DefaultAuditTopic {
		t.Errorf("topic: %q want %q", s.topic, DefaultAuditTopic)
	}
	_ = s.Close()
}

func TestKafkaSinkEmitNilWriter(t *testing.T) {
	s := &KafkaSink{topic: "t"}
	if err := s.Emit(context.Background(), Event{ScreenID: "s", TxID: "tx"}); err == nil {
		t.Fatal("expected error for nil writer")
	}
	if s.Sent() != 0 {
		t.Errorf("sent: %d", s.Sent())
	}
}

func TestDBSinkCloseIsNoOp(t *testing.T) {
	s := NewDBSink(nil)
	if err := s.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

func TestDBSinkEmitError(t *testing.T) {
	// Use a *sql.DB backed by a connector that errors on connect, so Emit's
	// ExecContext fails fast without a real database or driver import.
	db := sql.OpenDB(errConnector{})
	defer db.Close()
	s := NewDBSink(db)
	if err := s.Emit(context.Background(), Event{ScreenID: "s1"}); err == nil {
		t.Fatal("expected error from failing connector")
	}
}

type errConnector struct{}

func (errConnector) Connect(_ context.Context) (driver.Conn, error) {
	return nil, errors.New("connect failed")
}
func (errConnector) Driver() driver.Driver { return errDriverInstance{} }

type errDriverInstance struct{}

func (errDriverInstance) Open(_ string) (driver.Conn, error) {
	return nil, errors.New("open failed")
}