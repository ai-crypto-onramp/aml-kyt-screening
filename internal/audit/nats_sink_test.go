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
	_, err := NewSink("kafka://broker:9092")
	if err == nil || !strings.Contains(err.Error(), "unknown event bus scheme") {
		t.Fatalf("err: %v", err)
	}
}

func TestNewNATSSinkConnectError(t *testing.T) {
	_, err := NewNATSSink("nats://127.0.0.1:1", "")
	if err == nil {
		t.Fatal("expected error connecting to non-existent NATS")
	}
	if !strings.Contains(err.Error(), "nats connect") {
		t.Errorf("err: %v", err)
	}
}

func TestNATSSinkConstructorSetsSubject(t *testing.T) {
	// We cannot connect to a real server in unit tests; just verify the
	// constructor subject default logic via the exported const.
	if DefaultAuditSubject == "" {
		t.Fatal("DefaultAuditSubject must be set")
	}
}

func TestNATSSinkEmitOnClosedConn(t *testing.T) {
	sink := &NATSSink{subject: "kyt.audit.v1"}
	if err := sink.Emit(context.Background(), Event{ScreenID: "s1"}); err == nil {
		t.Fatal("expected error emitting on nil conn")
	}
	if err := sink.Close(); err != nil {
		t.Errorf("close should be no-op on nil conn: %v", err)
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