package ai

import (
	"errors"
	"sync"
	"time"
)

// ErrCircuitOpen is returned when the circuit breaker is in the open state.
var ErrCircuitOpen = errors.New("circuit breaker is open")

type cbState int

const (
	cbClosed   cbState = iota
	cbOpen
	cbHalfOpen
)

// circuitBreaker implements a simple in-process circuit breaker.
// It opens after consecutiveFailureThreshold consecutive failures and
// transitions to half-open after cooldown elapses.
type circuitBreaker struct {
	mu                sync.Mutex
	state             cbState
	consecutiveFails  int
	failureThreshold  int
	cooldown          time.Duration
	lastFailure       time.Time
}

func newCircuitBreaker(failureThreshold int, cooldown time.Duration) *circuitBreaker {
	return &circuitBreaker{
		failureThreshold: failureThreshold,
		cooldown:         cooldown,
	}
}

// allow checks whether a request is allowed. Returns false when the circuit
// is open and the cooldown has not elapsed.
func (cb *circuitBreaker) allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case cbClosed:
		return true
	case cbHalfOpen:
		return true
	case cbOpen:
		if time.Since(cb.lastFailure) >= cb.cooldown {
			cb.state = cbHalfOpen
			return true
		}
		return false
	}
	return true
}

// recordSuccess resets the circuit breaker to the closed state.
func (cb *circuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.consecutiveFails = 0
	cb.state = cbClosed
}

// recordFailure increments the failure counter and may open the circuit.
func (cb *circuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.consecutiveFails++
	cb.lastFailure = time.Now()
	if cb.consecutiveFails >= cb.failureThreshold {
		cb.state = cbOpen
	}
}
