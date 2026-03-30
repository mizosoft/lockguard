package a

import "sync"

type rw struct {
	readers int `read_protected_by:"mu"`
	writers int `write_protected_by:"mu"`
	auto    int `protected_by:"mu"`
	mu      sync.RWMutex
}

func (r *rw) multipleRLocks() {
	r.mu.RLock()
	_ = r.readers // OK
	_ = r.auto    // OK
	r.mu.RLock()  // Double RLock (allowed in Go)
	_ = r.readers // OK
	r.mu.RUnlock()
	_ = r.readers // OK (still one RLock held)
	r.mu.RUnlock()

	_ = r.readers // want `mu is not held while accessing readers`
}

func (r *rw) upgradeLockPattern() {
	r.mu.RLock()
	_ = r.readers // OK
	r.mu.RUnlock()

	// Common pattern: unlock read, lock write
	r.mu.Lock()
	r.writers++ // OK
	r.mu.Unlock()
}

func (r *rw) mixedAccess() {
	r.mu.RLock()
	_ = r.readers // OK
	r.writers++   // want `mu is not held while accessing writers`
	r.auto++      // want `mu is not held while accessing auto`
	r.mu.RUnlock()
}

type rwLevels struct {
	auto_rw int `protected_by:"mut"`
	r       int `read_protected_by:"mut"`
	w       int `write_protected_by:"mut"`
	rw      int `rw_protected_by:"mut"`
	mut     sync.RWMutex
}

func (s *rwLevels) readWriteLockUnlock() {
	s.auto_rw++   // want `mut is not held while accessing auto_rw`
	_ = s.auto_rw // want `mut is not held while accessing auto_rw`
	s.r++         // want `mut is not held while accessing r`
	_ = s.r       // want `mut is not held while accessing r`
	s.w++         // want `mut is not held while accessing w`
	_ = s.w       // want `mut is not held while accessing w`
	s.rw++        // want `mut is not held while accessing rw`
	_ = s.rw      // want `mut is not held while accessing rw`

	s.mut.RLock()
	_ = s.auto_rw
	s.mut.RUnlock()

	s.mut.RLock()
	s.auto_rw++ // want `mut is not held while accessing auto_rw`
	s.mut.RUnlock()

	s.mut.Lock()
	s.auto_rw++
	s.mut.Unlock()

	s.mut.RLock()
	s.r++
	s.mut.RUnlock()

	s.mut.Lock()
	s.r++
	s.mut.Unlock()

	s.mut.RLock()
	s.w++ // want `mut is not held while accessing w`
	s.mut.RUnlock()

	s.mut.Lock()
	s.w++
	s.mut.Unlock()

	s.mut.RLock()
	s.rw++
	s.mut.RUnlock()

	s.mut.Lock()
	s.rw++
	s.mut.Unlock()
}
