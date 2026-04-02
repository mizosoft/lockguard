package a

import "sync"

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
	l.mu.Lock()
	l.data++ // OK
} // want `'l\.mu' held at function exit \(lock leak\)`

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

// Lock leaked on all early-return paths.
func (l *leakTest) earlyReturnLeak(cond bool) {
	l.mu.Lock()
	if cond {
		return // lock still held → leak
	}
	l.mu.Unlock()
} // want `'l\.mu' held at function exit \(lock leak\)`

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
	r.mu.RLock()
	_ = r.data // OK
} // want `read lock on 'r\.mu' held at function exit \(lock leak\)`

// Write-lock acquired but never released.
func (r *rwLeakTest) writeLockLeak() {
	r.mu.Lock()
	r.data++
} // want `'r\.mu' held at function exit \(lock leak\)`

// Proper RLock/RUnlock — no warning.
func (r *rwLeakTest) noReadLockLeak() {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_ = r.data
}
