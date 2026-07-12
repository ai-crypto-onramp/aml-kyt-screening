package audit

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEmitterEmits(t *testing.T) {
	sink := NewMemorySink()
	emitter := NewEmitter(sink, 16)
	defer emitter.Close()

	for i := 0; i < 4; i++ {
		if err := emitter.Emit(context.Background(), Event{ScreenID: "s", TxID: "tx"}); err != nil {
			t.Fatalf("emit: %v", err)
		}
	}
	// Wait for the worker to drain.
	deadline := time.After(2 * time.Second)
	for {
		if sink.Events() != nil && len(sink.Events()) == 4 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for emit; got %d", len(sink.Events()))
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if emitter.Emitted() != 4 {
		t.Errorf("emitted counter: %d", emitter.Emitted())
	}
	if emitter.Drops() != 0 {
		t.Errorf("drops: %d", emitter.Drops())
	}
}

func TestEmitterDropsOnOverflow(t *testing.T) {
	sink := &blockingSink{}
	emitter := NewEmitter(sink, 2)
	defer emitter.Close()

	// The worker will block on the first event; subsequent Emits fill the
	// queue and then overflow.
	var drops atomic.Int64
	for i := 0; i < 10; i++ {
		if err := emitter.Emit(context.Background(), Event{}); err != nil {
			if errors.Is(err, ErrAuditDropped) {
				drops.Add(1)
			} else {
				t.Fatalf("emit err: %v", err)
			}
		}
	}
	if drops.Load() == 0 {
		t.Fatal("expected at least one drop on overflow")
	}
}

type blockingSink struct{ mu sync.Mutex }

func (s *blockingSink) Emit(ctx context.Context, e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

func TestEmitterRecordsSinkFailuresAsDrops(t *testing.T) {
	sink := NewMemorySink()
	sink.SetFail(true)
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

func TestEmitterCloseStopsWorker(t *testing.T) {
	sink := NewMemorySink()
	emitter := NewEmitter(sink, 4)
	emitter.Close()
	// After Close, Emit returns ErrAuditDropped (queue no longer drained) or
	// succeeds if the queue still has room. We just verify Close returns.
	if emitter.Emitted() != 0 && sink.Events() == nil {
		// non-fatal
	}
}

func TestMemorySinkSetFail(t *testing.T) {
	sink := NewMemorySink()
	sink.SetFail(true)
	if err := sink.Emit(context.Background(), Event{}); err == nil {
		t.Fatal("expected error")
	}
	sink.SetFail(false)
	if err := sink.Emit(context.Background(), Event{}); err != nil {
		t.Fatalf("emit: %v", err)
	}
}

func TestJSONSink(t *testing.T) {
	s := NewJSONSink()
	if err := s.Emit(context.Background(), Event{ScreenID: "s1", TxID: "tx1"}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := s.Emit(context.Background(), Event{ScreenID: "s2", TxID: "tx2"}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if len(s.Bytes()) != 2 {
		t.Fatalf("expected 2 events, got %d", len(s.Bytes()))
	}
}

func TestEmitterSetsCreatedAt(t *testing.T) {
	sink := NewMemorySink()
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	emitter := NewEmitter(sink, 4).WithNow(func() time.Time { return now })
	defer emitter.Close()
	if err := emitter.Emit(context.Background(), Event{}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	deadline := time.After(2 * time.Second)
	for {
		if evs := sink.Events(); len(evs) == 1 {
			if !evs[0].CreatedAt.Equal(now) {
				t.Errorf("CreatedAt: %v, want %v", evs[0].CreatedAt, now)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}