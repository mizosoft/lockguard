package a

import (
	"math/rand"
	"sync"
)

// ============================================================================
// Lock-leak detection: locks that are held at function exit
// ============================================================================

type leakTest struct {
	data int `protected_by:"mu"`
	mu   sync.Mutex
}

// ============================================================================
// Simple cases
// ============================================================================

// Lock acquired but never released.
func (l *leakTest) simpleLeak() {
	l.mu.Lock() // want `'l\.mu' acquired but never unlocked`
	l.data++    // OK
}

// No lock — no leak warning (only an access warning).
func (l *leakTest) noLock() {
	_ = l.data // want `reading 'l\.data' requires holding 'l\.mu'`
}

// Lock acquired and released — no warning.
func (l *leakTest) noLeak() {
	l.mu.Lock()
	l.data++
	l.mu.Unlock()
}

// Lock acquired and released with defer — no warning.
func (l *leakTest) noLeakDefer() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.data++
}

// ============================================================================
// Early-return paths
// ============================================================================

// Lock leaked on the early-return path, but released on the normal path.
func (l *leakTest) earlyReturnLeak(cond bool) {
	l.mu.Lock()
	if cond {
		return // lock still held → leak
	}
	l.mu.Unlock()
} // want `'l\.mu' may not be unlocked at function exit`

// Deferred unlock covers all paths — no warning.
func (l *leakTest) earlyReturnDeferred(cond bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if cond {
		return // defer fires → no leak
	}
}

// ============================================================================
// RWMutex
// ============================================================================

type rwLeakTest struct {
	data int `protected_by:"mu"`
	mu   sync.RWMutex
}

// Read-lock acquired but never released.
func (r *rwLeakTest) readLockLeak() {
	r.mu.RLock() // want `read lock on 'r\.mu' acquired but never unlocked`
	_ = r.data   // OK
}

// Write-lock acquired but never released.
func (r *rwLeakTest) writeLockLeak() {
	r.mu.Lock() // want `'r\.mu' acquired but never unlocked`
	r.data++
}

// Proper RLock/RUnlock — no warning.
func (r *rwLeakTest) noReadLockLeak() {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_ = r.data
}

func (s *S1) conditionalDeferredUnlock() {
	s.mut.Lock()
	defer func() {
		if rand.Int() == 1 {
			s.mut.Unlock()
		}
	}()
	s.i++
} // want `'s\.mut' may not be unlocked at function exit`

func (s *S1) conditionalDeferredUnlockWithBranches() {
	s.mut.Lock()
	if true {
		defer func() {
			if rand.Int() == 1 {
				s.mut.Unlock()
			}
		}()
	} else {
		defer func() {
			if rand.Int() == 3 {
				s.mut.Unlock()
			}
		}()
	}
	s.i++
} // want `'s\.mut' may not be unlocked at function exit`
