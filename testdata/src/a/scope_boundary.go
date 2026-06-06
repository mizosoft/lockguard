package a

import "sync"

// ============================================================================
// Scope-boundary lock-leak detection: locks held when a variable goes out of scope.
// ============================================================================

// ============================================================================
// Block-scoped mutex — basic cases
// ============================================================================

// Lock acquired and held when the block scope closes.
func blockScopeLeak() {
	{
		var mu sync.Mutex
		mu.Lock() // want `'mu' acquired but never unlocked`
	}
}

// Lock acquired and released before scope closes — no warning.
func blockScopeNoLeak() {
	{
		var mu sync.Mutex
		mu.Lock()
		mu.Unlock()
	}
}

// Lock released via defer — defer fires at function exit, not block exit, but the deferred
// unlock properly releases the lock before exit. No leak.
func blockScopeDeferLeak() {
	{
		var mu sync.Mutex
		mu.Lock()
		defer mu.Unlock()
	}
}

// RLock acquired but never released.
func blockScopeRLockLeak() {
	{
		var mu sync.RWMutex
		mu.RLock() // want `read lock on 'mu' acquired but never unlocked`
		_ = 1
	}
}

// ============================================================================
// Block-scoped struct holding a mutex
// ============================================================================

type sbTest struct {
	data int `protected_by:"mu"`
	mu   sync.Mutex
}

// Lock on a block-scoped struct acquired but never released.
func blockScopeStructLeak() {
	{
		var s sbTest
		s.mu.Lock() // want `'s\.mu' acquired but never unlocked`
		s.data++    // OK
	}
}

// Lock released before scope close — no warning.
func blockScopeStructNoLeak() {
	{
		var s sbTest
		s.mu.Lock()
		s.data++
		s.mu.Unlock()
	}
}

// ============================================================================
// Lock from outer scope — block close must NOT warn
// ============================================================================

// The mutex belongs to the outer receiver — closing an inner block must not emit a leak.
func (s *sbTest) innerBlockOuterLock() {
	s.mu.Lock()
	{
		s.data++ // OK
	}
	s.mu.Unlock()
}

// ============================================================================
// Conditional acquisition in a block scope
// ============================================================================

// Lock conditionally acquired — possibly held at function exit.
func blockScopeConditionalLeak(cond bool) {
	{
		var mu sync.Mutex
		if cond {
			mu.Lock()
		}
		_ = 1
	}
} // want `'mu' may not be unlocked at function exit`

// ============================================================================
// If-body scope
// ============================================================================

// Lock declared and held inside an if body. Because cond may be false (lock never acquired),
// the lock may not be held at function exit.
func ifBodyScopeLeak(cond bool) {
	if cond {
		var mu sync.Mutex
		mu.Lock()
		_ = 1
	}
} // want `'mu' may not be unlocked at function exit`

// Lock declared, acquired and released inside an if body — no warning.
func ifBodyScopeNoLeak(cond bool) {
	if cond {
		var mu sync.Mutex
		mu.Lock()
		mu.Unlock()
	}
}

// ============================================================================
// Statement-header scope (if/for/switch init variable)
// ============================================================================

// Lock declared in an if-init statement. Since the condition may be false, the lock may
// or may not be held at function exit.
func ifInitScopeLeak(cond bool) {
	if mu := new(sync.Mutex); cond {
		mu.Lock()
		_ = 1
	}
} // want `'mu' may not be unlocked at function exit`

// Lock declared in an if-init, acquired unconditionally inside the body. Static analysis
// treats the condition as potentially false, so the lock may not be held at function exit.
func ifInitAlwaysLocked() {
	if mu := new(sync.Mutex); true {
		mu.Lock()
	}
} // want `'mu' may not be unlocked at function exit`

// Lock declared in an if-init and released before scope exits — no warning.
func ifInitNoLeak(cond bool) {
	if mu := new(sync.Mutex); cond {
		mu.Lock()
		mu.Unlock()
	}
}

// Lock declared in if-init with TryLock directly as the condition — the canonical pattern.
// If TryLock succeeded the lock is possibly held at function exit.
func ifInitTryLockScopeLeak() {
	if mu := new(sync.Mutex); mu.TryLock() {
		_ = 1
	}
} // want `'mu' may not be unlocked at function exit`

// ============================================================================
// For-body scope
// ============================================================================

// For-loop body scope: the back-edge from the post block to the condition block is skipped
// by the DFS, so the loop body's lock state never reaches a recorded function exit.
// Limitations: for-body scoped leaks are not currently detected.
func forBodyScopeLeak() {
	for i := 0; i < 10; i++ {
		var mu sync.Mutex
		mu.Lock()
		_ = i
	} // no warning (DFS back-edge skipping prevents detecting this pattern)
}

func forBodyScopeNoLeak() {
	for i := 0; i < 10; i++ {
		var mu sync.Mutex
		mu.Lock()
		mu.Unlock()
		_ = i
	}
}
