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
		s.i++ // want `writing 's\.i' requires holding 's\.mut'`
	}

	// defer s.mut.Unlock() has not applied yet, so the lock is possibly still held.
	s.i++ // want `writing 's\.i' requires holding 's\.mut' \(not held on all paths\)`
}

func tryLockUnlockInstantly() {
	var s S
	if s.mut.TryLock() {
		s.i++
		s.mut.Unlock()
	} else {
		s.i++ // want `writing 's\.i' requires holding 's\.mut'`
	}

	s.i++ // want `writing 's\.i' requires holding 's\.mut'`
}

// TryLock with no else clause: lock is not held after the if because it was
// released inside the body.
func tryLockNoElse() {
	var s S
	if s.mut.TryLock() {
		s.i++
		s.mut.Unlock()
	}
	s.i++ // want `writing 's\.i' requires holding 's\.mut'`
}

// TryLock with no else and no unlock: lock is possibly held after the if,
// because it was acquired only on the true path and never released.
func tryLockNoElseNoUnlock() {
	var s S
	if s.mut.TryLock() {
		s.i++
	}
	s.i++ // want `writing 's\.i' requires holding 's\.mut' \(not held on all paths\)`
} // want `'s\.mut' possibly held at function exit \(possible lock leak\)`

// ============================================================================
// Negated TryLock
// ============================================================================

// In the true branch of !TryLock(), TryLock failed — the lock is not held.
// In the false branch (else), TryLock succeeded — the lock is held.
func negatedTryLock() {
	var s S
	if !s.mut.TryLock() {
		s.i++ // want `writing 's\.i' requires holding 's\.mut'`
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
		s.data++ // want `writing 's\.data' requires holding 's\.mu'`
	}
	s.data++ // want `writing 's\.data' requires holding 's\.mu'`
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
		t.a++ // want `writing 't\.a' requires holding 't\.mu1' \(not held on all paths\)`
		t.b++ // want `writing 't\.b' requires holding 't\.mu2' \(not held on all paths\)`
	}
} // want `'t\.mu1' possibly held at function exit \(possible lock leak\)` `'t\.mu2' possibly held at function exit \(possible lock leak\)`

// With ||, it is unknown which lock succeeded in the true branch, so both are
// only possibly held. In the false branch, neither was acquired.
func tryLockOr() {
	var t Two
	if t.mu1.TryLock() || t.mu2.TryLock() {
		t.a++ // want `writing 't\.a' requires holding 't\.mu1' \(not held on all paths\)`
		t.b++ // want `writing 't\.b' requires holding 't\.mu2' \(not held on all paths\)`
	} else {
		t.a++ // want `writing 't\.a' requires holding 't\.mu1'`
		t.b++ // want `writing 't\.b' requires holding 't\.mu2'`
	}
} // want `'t\.mu1' possibly held at function exit \(possible lock leak\)` `'t\.mu2' possibly held at function exit \(possible lock leak\)`

// ============================================================================
// Deadlock detection
// ============================================================================

// ============================================================================
// Early-return TryLock guard pattern
// ============================================================================

// The canonical guard pattern: if TryLock fails, return immediately.
// After the if, TryLock must have succeeded, so the lock is definitely held.
func earlyReturnGuard() {
	var s S
	if !s.mut.TryLock() {
		return
	}
	s.i++
	s.mut.Unlock()
}

// Same pattern but with deferred unlock.
func earlyReturnGuardDeferred() {
	var s S
	if !s.mut.TryLock() {
		return
	}
	defer s.mut.Unlock()
	s.i++
}

// The non-negated form: the else branch is the success path.
// (This is already handled via KindIfElse and is tested above in negatedTryLock.)

// Chained guard: two locks both acquired via early-return guards.
func earlyReturnGuardTwoLocks() {
	var t Two
	if !t.mu1.TryLock() {
		return
	}
	if !t.mu2.TryLock() {
		t.mu1.Unlock() // OK
		return
	}
	t.a++          // OK
	t.b++          // OK
	t.mu1.Unlock() // OK
	t.mu2.Unlock() // OK
}

// TryLocking a lock that is already held is a deadlock.
func doubleTryLock() {
	var s S
	if s.mut.TryLock() {
		if s.mut.TryLock() { // want `acquiring 'mut' that is already held \[deadlock\]`
			s.mut.Unlock()
		}
		s.i++
		s.mut.Unlock()
	}
}
