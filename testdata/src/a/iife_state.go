package a

import "sync"

// ============================================================================
// Statement-level inline IIFE (func(){ ... }()) decompression.
//
// A statement-level inline function literal is treated as a compressed node in the enclosing CFG:
// its lock effects on enclosing-scope variables flow into the rest of the function (state
// continuation), while locks on variables it declares itself are leak-checked at the literal and
// do not escape. Each of the literal's exit paths continues the enclosing function independently,
// so branchy literals stay fully path-sensitive.
// ============================================================================

type iifeLocal struct {
	mu sync.Mutex
}

// A lock taken on the receiver (declared by the enclosing method) flows out of the literal: the
// access after the literal is protected, and the leak surfaces for the enclosing function.
func (s *S1) iifeReceiverLockFlows() {
	func() {
		s.mut.Lock() // want `'s\.mut' acquired but never unlocked`
	}()
	s.i++ // OK — s.mut flowed out of the literal.
}

// Lock plus deferred unlock fully inside the literal: the unlock runs at the literal's exit, so
// nothing flows out and the access after the literal is unprotected.
func (s *S1) iifeLockUnlockInside() {
	func() {
		s.mut.Lock()
		defer s.mut.Unlock()
		s.i++ // OK — held inside the literal.
	}()
	s.i++ // want `writing 's\.i' requires holding 's\.mut'`
}

// A lock on a variable declared inside the literal is owned by the literal: its leak is reported at
// the literal and is pruned at the seam, so it never flows to the enclosing function.
func iifeLocalLeak() {
	func() {
		var m iifeLocal
		m.mu.Lock() // want `'m\.mu' acquired but never unlocked`
		_ = m
	}()
}

// Branchy literal: the enclosing-scope lock is taken on only one of the literal's exit paths, so
// the state flowing out is uncertain — the post-literal access is possibly-missing and the leak is
// uncertain. This exercises per-exit-path continuation (no state merging).
func (s *S1) iifeConditionalLockFlows(cond bool) {
	func() {
		if cond {
			s.mut.Lock()
		}
	}()
	s.i++ // want `writing 's\.i' requires holding 's\.mut' \(not held on all paths\)`
} // want `'s\.mut' may not be unlocked at function exit`
