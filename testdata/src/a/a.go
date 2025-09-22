package a

import (
	"fmt"
	"sync"
)

type S1 struct {
	i   int `protected_by:"mut"`
	mut sync.Mutex
}

// This is a function called fn.
// @lockguard `protected_by:"s.mut"`
func (s *S1) fn() {

}

// This is a function called Func.
func Func() {
	s1 := S1{}
	//s1.i++

	//func() {
	//	s1.mut.Lock()
	//	defer func() {
	//		f := func() func() {
	//			return s1.mut.Unlock
	//		}
	//		f()
	//	}()
	//}()

	func() {
		s1.mut.Lock()
		defer s1.mut.Unlock()

		s1.i++
	}()

	s1.mut.Lock()

	func() {
		s1.i++
	}()

	s1.mut.Unlock()

	s1.mut.Lock()
	fn := func() {
		s1.i++
	}
	s1.mut.Unlock()

	fmt.Println(fn)

	//s1.mut.Lock()
	//s1.i++
	//s1.mut.Unlock()
	//s1.i++
}
