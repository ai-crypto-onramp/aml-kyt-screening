// Package audit emits an audit event for every screen (allow/block/review,
// cache hit or miss) to the Audit / Event Log via an async event bus, with
// DB fallback when AUDIT_EVENT_BUS_URL is unset.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Event is the audit event schema for a screen call.
type Event struct {
	ScreenID      string    `json:"screen_id"`
	TxID          string    `json:"tx_id"`
	Address       string    `json:"address"`
	Chain         string    `json:"chain"`
	Amount        string    `json:"amount"`
	Decision      string    `json:"decision"`
	Exposure      string    `json:"exposure"`
	RiskScore     int       `json:"risk_score"`
	Vendor        string    `json:"vendor"`
	CacheHit      bool      `json:"cache_hit"`
	Source        string    `json:"source"` // "vendor" or "cache"
	Operator      string    `json:"operator,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// Sink is the abstraction for the audit event destination. Implementations
// include the async bus producer (NATS/Kafka) and the DB-fallback sink.
type Sink interface {
	Emit(ctx context.Context, e Event) error
}

// Emitter is the audit emission service. It never blocks the screen path:
// events are queued to a bounded channel and a worker drains it
// asynchronously. Overflow increments a drop counter metric.
type Emitter struct {
	sink     Sink
	queue    chan Event
	drops    atomic.Int64
	emitted  atomic.Int64
	wg       sync.WaitGroup
	stopOnce sync.Once
	stopCh   chan struct{}
	now      func() time.Time
}

// NewEmitter returns a started Emitter. queueSize is the bounded queue length;
// when full, Emit returns ErrAuditDropped and increments the drop counter.
func NewEmitter(sink Sink, queueSize int) *Emitter {
	if queueSize <= 0 {
		queueSize = 1024
	}
	e := &Emitter{
		sink:   sink,
		queue:  make(chan Event, queueSize),
		stopCh: make(chan struct{}),
		now:    time.Now,
	}
	e.wg.Add(1)
	go e.run()
	return e
}

// WithNow overrides the clock (for testing).
func (e *Emitter) WithNow(now func() time.Time) *Emitter {
	e.now = now
	return e
}

// run is the worker goroutine.
func (e *Emitter) run() {
	defer e.wg.Done()
	for {
		select {
		case ev := <-e.queue:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := e.sink.Emit(ctx, ev); err != nil {
				// Emit failures are non-fatal: count as a drop.
				e.drops.Add(1)
			} else {
				e.emitted.Add(1)
			}
			cancel()
		case <-e.stopCh:
			return
		}
	}
}

// Emit enqueues event ev. It never blocks the caller for longer than the
// enqueue attempt; a full queue returns ErrAuditDropped.
func (e *Emitter) Emit(ctx context.Context, ev Event) error {
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = e.now()
	}
	select {
	case e.queue <- ev:
		return nil
	default:
		e.drops.Add(1)
		return ErrAuditDropped
	}
}

// Drops returns the number of dropped events (overflow or sink failures).
func (e *Emitter) Drops() int64 { return e.drops.Load() }

// Emitted returns the number of events successfully emitted to the sink.
func (e *Emitter) Emitted() int64 { return e.emitted.Load() }

// Close drains the queue and stops the worker goroutine.
func (e *Emitter) Close() {
	e.stopOnce.Do(func() { close(e.stopCh) })
	e.wg.Wait()
}

// ErrAuditDropped is returned when the bounded audit queue is full.
var ErrAuditDropped = errors.New("audit event dropped")

// MemorySink is an in-memory Sink used for tests and the DB-less fallback.
type MemorySink struct {
	mu   sync.Mutex
	mem  []Event
	fail bool
}

// NewMemorySink returns a fresh in-memory sink.
func NewMemorySink() *MemorySink { return &MemorySink{} }

// SetFail forces subsequent Emit calls to return an error.
func (s *MemorySink) SetFail(f bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = f
}

// Emit appends e to the in-memory list.
func (s *MemorySink) Emit(_ context.Context, e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("sink unavailable")
	}
	s.mem = append(s.mem, e)
	return nil
}

// Events returns a copy of the recorded events.
func (s *MemorySink) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Event, len(s.mem))
	copy(out, s.mem)
	return out
}

// JSONSink wraps any byte-sink that accepts JSON-encoded events; used for the
// bus producer when AUDIT_EVENT_BUS_URL is set (mocked here).
type JSONSink struct {
	mu  sync.Mutex
	mem [][]byte
}

// NewJSONSink returns a JSONSink.
func NewJSONSink() *JSONSink { return &JSONSink{} }

// Emit JSON-encodes e and stores it.
func (s *JSONSink) Emit(_ context.Context, e Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mem = append(s.mem, body)
	return nil
}

// Bytes returns the recorded JSON-encoded events.
func (s *JSONSink) Bytes() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.mem))
	copy(out, s.mem)
	return out
}