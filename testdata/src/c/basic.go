package c

import (
	"sync"
)

type S1 struct {
	i   int `protected_by:"mut"`
	j   int
	mut sync.Mutex
}

func (s *S1) mutFunc() *sync.Mutex {
	return &s.mut
}

func lockUnlock() {
	var s1 S1
	s1.i++ // want `writing 'i' requires holding 'mut'`

	s1.mut.Lock()
	s1.i++
	s1.mut.Unlock()

	s1.i++ // want `writing 'i' requires holding 'mut'`
}

func (s *S1) methodLockUnlock() {
	s.i++ // want `writing 'i' requires holding 'mut'`

	s.mut.Lock()
	s.i++
	s.mut.Unlock()

	s.i++ // want `writing 'i' requires holding 'mut'`
}

func lockDeferredUnlock() {
	var s1 S1
	s1.i++ // want `writing 'i' requires holding 'mut'`

	s1.mut.Lock()
	defer s1.mut.Unlock()
	s1.i++
}
