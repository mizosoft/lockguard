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

	_ = r.readers // want `reading 'r\.readers' requires holding 'r\.mu'`
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
	r.writers++   // want `writing 'r\.writers' requires holding 'r\.mu'`
	r.auto++      // want `writing 'r\.auto' requires holding 'r\.mu'`
	r.mu.RUnlock()
}

type rwLevels struct {
	auto_rw int `protected_by:"mut"`
	r       int `read_protected_by:"mut"`
	w       int `write_protected_by:"mut"`
	mut     sync.RWMutex
}

func (s *rwLevels) readWriteLockUnlock() {
	s.auto_rw++   // want `writing 's\.auto_rw' requires holding 's\.mut'`
	_ = s.auto_rw // want `reading 's\.auto_rw' requires holding 's\.mut'`
	s.r++         // want `writing 's\.r' requires holding 's\.mut'`
	_ = s.r       // want `reading 's\.r' requires holding 's\.mut'`
	s.w++         // want `writing 's\.w' requires holding 's\.mut'`
	_ = s.w       // want `reading 's\.w' requires holding 's\.mut'`

	s.mut.RLock()
	_ = s.auto_rw
	s.mut.RUnlock()

	s.mut.RLock()
	s.auto_rw++ // want `writing 's\.auto_rw' requires holding 's\.mut'`
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
	s.w++ // want `writing 's\.w' requires holding 's\.mut'`
	s.mut.RUnlock()

	s.mut.Lock()
	s.w++
	s.mut.Unlock()
}
