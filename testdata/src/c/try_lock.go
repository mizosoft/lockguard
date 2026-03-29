package c

import (
	"sync"
)

type S struct {
	i   int `protected_by:"mut"`
	mut sync.Mutex
}

func tryLockUnlockDeferred() {
	var s S
	if s.mut.TryLock() {
		defer s.mut.Unlock()
		s.i++
	} else {
		s.i++ // want `mut is not held while accessing i`
	}

	// Here, we don't know whether the lock is locked or unlocked, because defer s.mut.Unlock(),
	// is applied only after existing the function, so the lock might still be held.
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
