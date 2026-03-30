package a

import (
	"sync"
)

type S struct {
	i   int `protected_by:"mut"`
	mut sync.Mutex
}

// ============================================================================
// Basic TryLock
// ============================================================================

func tryLockUnlockDeferred() {
	var s S
	if s.mut.TryLock() {
		defer s.mut.Unlock()
		s.i++
	} else {
		s.i++ // want `mut is not held while accessing i`
	}

	// defer s.mut.Unlock() has not applied yet, so the lock is possibly still held.
	s.i++ // want `mut is possibly not held while accessing i`
}

func tryLockUnlockInstantly() {
	var s S
	if s.mut.TryLock() {
		s.i++
		s.mut.Unlock()
	} else {
		s.i++ // want `mut is not held while accessing i`
	}

	s.i++ // want `mut is not held while accessing i`
}

// TryLock with no else clause: lock is not held after the if because it was
// released inside the body.
func tryLockNoElse() {
	var s S
	if s.mut.TryLock() {
		s.i++
		s.mut.Unlock()
	}
	s.i++ // want `mut is not held while accessing i`
}

// TryLock with no else and no unlock: lock is possibly held after the if,
// because it was acquired only on the true path and never released.
func tryLockNoElseNoUnlock() {
	var s S
	if s.mut.TryLock() {
		s.i++
	}
	s.i++ // want `mut is possibly not held while accessing i`
}

// ============================================================================
// Negated TryLock
// ============================================================================

// In the true branch of !TryLock(), TryLock failed — the lock is not held.
// In the false branch (else), TryLock succeeded — the lock is held.
func negatedTryLock() {
	var s S
	if !s.mut.TryLock() {
		s.i++ // want `mut is not held while accessing i`
	} else {
		defer s.mut.Unlock()
		s.i++ // OK
	}
}

// ============================================================================
// TryRLock
// ============================================================================

type RWS struct {
	data int `read_protected_by:"mu"` // accepts either Lock or RLock
	mu   sync.RWMutex
}

// TryRLock in the true branch satisfies protected_by (RLock is sufficient).
func tryRLock() {
	var s RWS
	if s.mu.TryRLock() {
		s.data++ // OK
		s.mu.RUnlock()
	} else {
		s.data++ // want `mu is not held while accessing data`
	}
	s.data++ // want `mu is not held while accessing data`
}

// ============================================================================
// Compound TryLock conditions
// ============================================================================

type Two struct {
	a   int `protected_by:"mu1"`
	b   int `protected_by:"mu2"`
	mu1 sync.Mutex
	mu2 sync.Mutex
}

// With &&, both locks must succeed for the true branch, so both are
// definitely held there. In the false branch, either could have locked before
// the short-circuit, so both are possibly held.
func tryLockAnd() {
	var t Two
	if t.mu1.TryLock() && t.mu2.TryLock() {
		t.a++ // OK
		t.b++ // OK
		t.mu1.Unlock()
		t.mu2.Unlock()
	} else {
		t.a++ // want `mu1 is possibly not held while accessing a`
		t.b++ // want `mu2 is possibly not held while accessing b`
	}
}

// With ||, it is unknown which lock succeeded in the true branch, so both are
// only possibly held. In the false branch, neither was acquired.
func tryLockOr() {
	var t Two
	if t.mu1.TryLock() || t.mu2.TryLock() {
		t.a++ // want `mu1 is possibly not held while accessing a`
		t.b++ // want `mu2 is possibly not held while accessing b`
	} else {
		t.a++ // want `mu1 is not held while accessing a`
		t.b++ // want `mu2 is not held while accessing b`
	}
}

// ============================================================================
// Deadlock detection
// ============================================================================

// TryLocking a lock that is already held is a deadlock.
func doubleTryLock() {
	var s S
	if s.mut.TryLock() {
		if s.mut.TryLock() { // want `deadlock: mut - already locked`
			s.mut.Unlock()
		}
		s.i++
		s.mut.Unlock()
	}
}
