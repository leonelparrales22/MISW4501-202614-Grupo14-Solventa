package main

import (
	"testing"
	"time"
)

func TestCircuitBreakerOpensAndRecovers(t *testing.T) {
	b := &circuitBreaker{state: closed, threshold: 2, halfOpenTarget: 2, recovery: time.Millisecond}
	b.failure()
	if b.current() != closed {
		t.Fatal("opened too early")
	}
	b.failure()
	if b.current() != open {
		t.Fatal("did not open")
	}
	if b.allow() != errCircuitOpen {
		t.Fatal("open circuit allowed request")
	}
	time.Sleep(2 * time.Millisecond)
	if b.allow() != nil || b.current() != halfOpen {
		t.Fatal("did not transition to half-open")
	}
	if b.allow() != errCircuitOpen {
		t.Fatal("half-open allowed concurrent trial")
	}
	b.success()
	if b.current() != halfOpen {
		t.Fatal("closed after too few half-open successes")
	}
	if b.allow() != nil {
		t.Fatal("did not allow next sequential half-open trial")
	}
	b.success()
	if b.current() != closed {
		t.Fatal("did not close")
	}
}
