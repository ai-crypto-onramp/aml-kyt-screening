package audit

import (
	"context"
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
	sink := &KafkaSink{topic: "kyt.audit.v1"}
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