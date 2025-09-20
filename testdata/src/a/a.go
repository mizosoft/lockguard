package a

import "sync"

type S1 struct {
	i   int `protected_by:"mut"`
	mut sync.Mutex
}

// Func This is a function
func Func() {
	s1 := S1{}

	s1.i++

	s1.mut.Lock()
	s1.i++
	s1.mut.Unlock()
	s1.i++
}
