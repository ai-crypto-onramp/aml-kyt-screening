package vendor

import (
	"context"
	"errors"
	"sync"
	"time"
)

// CircuitState enumerates breaker states.
type CircuitState int

const (
	CircuitClosed CircuitState = iota
	CircuitOpen
	CircuitHalfOpen
)

// CircuitBreaker opens after Threshold consecutive failures and stays open for
// OpenFor before transitioning to half-open. While open, calls return
// ErrVendorUnavailable without invoking the underlying provider.
type CircuitBreaker struct {
	Threshold int
	OpenFor   time.Duration

	mu          sync.Mutex
	state       CircuitState
	failures    int
	openedAt    time.Time
	allowedOnce bool
}

// NewCircuitBreaker returns a breaker that opens after threshold consecutive
// failures and re-closes after openFor has elapsed.
func NewCircuitBreaker(threshold int, openFor time.Duration) *CircuitBreaker {
	if threshold <= 0 {
		threshold = 5
	}
	if openFor <= 0 {
		openFor = 60 * time.Second
	}
	return &CircuitBreaker{Threshold: threshold, OpenFor: openFor}
}

// Allow reports whether a call may proceed. If the breaker is open and the
// OpenFor window has elapsed, it transitions to half-open and allows a single
// probe call.
func (b *CircuitBreaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case CircuitClosed:
		return true
	case CircuitOpen:
		if time.Since(b.openedAt) >= b.OpenFor {
			b.state = CircuitHalfOpen
			b.allowedOnce = false
		} else {
			return false
		}
		fallthrough
	case CircuitHalfOpen:
		if b.allowedOnce {
			return false
		}
		b.allowedOnce = true
		return true
	}
	return true
}

// State returns the current state (thread-safe).
func (b *CircuitBreaker) State() CircuitState {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Refresh open -> half-open transition for an accurate read.
	if b.state == CircuitOpen && time.Since(b.openedAt) >= b.OpenFor {
		b.state = CircuitHalfOpen
		b.allowedOnce = false
	}
	return b.state
}

// RecordSuccess resets the failure counter and closes the breaker.
func (b *CircuitBreaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.state = CircuitClosed
	b.allowedOnce = false
}

// RecordFailure increments the failure counter; when it reaches Threshold the
// breaker opens.
func (b *CircuitBreaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.state == CircuitHalfOpen || b.failures >= b.Threshold {
		b.state = CircuitOpen
		b.openedAt = time.Now()
		b.allowedOnce = false
	}
}

// ErrCircuitOpen is returned by Execute when the breaker is open.
var ErrCircuitOpen = errors.New("circuit breaker open")

// Execute runs fn under the breaker. If the breaker is open it returns
// ErrCircuitOpen without calling fn. On fn success the breaker is reset; on
// fn error the breaker records a failure.
func (b *CircuitBreaker) Execute(ctx context.Context, fn func(context.Context) error) error {
	if !b.Allow() {
		return ErrCircuitOpen
	}
	if err := fn(ctx); err != nil {
		b.RecordFailure()
		return err
	}
	b.RecordSuccess()
	return nil
}