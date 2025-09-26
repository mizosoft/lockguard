package a

import (
	"fmt"
	"sync"
)

type S1 struct {
	i   int `protected_by:"mut"`
	j   int
	mut sync.Mutex
}

func lockUnlock() {
	var s1 S1
	s1.i++ // want `mut is not held while accessing i`

	s1.mut.Lock()
	s1.i++
	s1.mut.Unlock()

	s1.i++ // want `mut is not held while accessing i`
}

func (s *S1) methodLockUnlock() {
	s.i++ // want `mut is not held while accessing i`

	s.mut.Lock()
	s.i++
	s.mut.Unlock()

	s.i++ // want `mut is not held while accessing i`
}

func lockDeferredUnlock() {
	var s1 S1
	s1.i++ // want `mut is not held while accessing i`

	s1.mut.Lock()
	defer s1.mut.Unlock()
	s1.i++
}

func (s *S1) methodLockUnlockDeferred() {
	s.i++ // want `mut is not held while accessing i`

	s.mut.Lock()
	defer s.mut.Unlock()
	s.i++
}

func accessNonProtectedField() {
	var s1 S1
	s1.j++
}

func (s *S1) methodAccessNonProtectedFields() {
	s.j++
}

func funcLiteralAcquiresNewScope() {
	var s1 S1
	s1.mut.Lock()
	fn := func() {
		s1.i++ // want `mut is not held while accessing i`

		s1.mut.Lock()
		s1.i++
		s1.mut.Unlock()
	}
	s1.mut.Unlock()
	fmt.Println(fn) // Consume
}

func (s *S1) methodFuncLiteralAcquiresNewScope() {
	s.mut.Lock()
	fn := func() {
		s.i++ // want `mut is not held while accessing i`

		s.mut.Lock()
		s.i++
		s.mut.Unlock()
	}
	s.mut.Unlock()
	fmt.Println(fn) // Consume
}

func funcLiteralInCallMaintainsScope() {
	var s1 S1
	s1.mut.Lock()
	func() {
		s1.i++ // Inline functions retain current lock scope
	}()
	s1.mut.Unlock()
}

func (s *S1) funcLiteralInCallMaintainsScope() {
	s.mut.Lock()
	func() {
		s.i++ // Inline functions retain current lock scope
	}()
	s.mut.Unlock()
}

type S2 struct {
	s1 S1
	k  int `protected_by:"s1.mut"`
}

func nestedLockUnlock() {
	var s2 S2
	s2.s1.i++ // want `mut is not held while accessing i`

	s2.s1.mut.Lock()
	s2.s1.i++
	s2.s1.mut.Unlock()

	s2.s1.i++ // want `mut is not held while accessing i`
}

func (s *S2) methodNestedLockUnlock() {
	s.s1.i++ // want `mut is not held while accessing i`
	s.k++    // want `mut is not held while accessing k`

	s.s1.mut.Lock()
	s.s1.i++
	s.k++
	s.s1.mut.Unlock()

	s.s1.i++ // want `mut is not held while accessing i`
	s.k++    // want `mut is not held while accessing k`
}

func nestedLockUnlockDeferred() {
	var s2 S2
	s2.s1.i++ // want `mut is not held while accessing i`
	s2.k++    // want `mut is not held while accessing k`

	s2.s1.mut.Lock()
	defer s2.s1.mut.Unlock()
	s2.s1.i++
	s2.k++
}

func (s *S2) methodNestedLockUnlockDeferred() {
	s.s1.i++ // want `mut is not held while accessing i`
	s.k++    // want `mut is not held while accessing k`

	s.s1.mut.Lock()
	defer s.s1.mut.Unlock()
	s.s1.i++
	s.k++
}

func lockUnlockWithScopes() {
	var s2 S2
	{
		if true {
			switch 1 {
			case 1:
				if 1 < 2 {
					for {
						if s2.s1.i > 0 { // want `mut is not held while accessing i`
						} else if s2.k > 0 { // want `mut is not held while accessing k`
						}
					}

					s2.s1.mut.Lock()
					for {
						switch 1 {
						case 1:
							s2.s1.i++
							s2.k++
						}
					}
					s2.s1.mut.Unlock()
				}

				s2.s1.i++ // want `mut is not held while accessing i`
				s2.k++    // want `mut is not held while accessing k`
			}
		}
		s2.s1.i++ // want `mut is not held while accessing i`
		s2.k++    // want `mut is not held while accessing k`
	}
}

func (s *S2) methodLockUnlockWithScopes() {
	{
		if true {
			switch 1 {
			case 1:
				if 1 < 2 {
					for {
						if s.s1.i > 0 { // want `mut is not held while accessing i`
						} else if s.k > 0 { // want `mut is not held while accessing k`
						}
					}

					s.s1.mut.Lock()
					for {
						switch 1 {
						case 1:
							s.s1.i++
							s.k++
						}
					}
					s.s1.mut.Unlock()
				}

				s.s1.i++ // want `mut is not held while accessing i`
				s.k++    // want `mut is not held while accessing k`
			}
		}
		s.s1.i++ // want `mut is not held while accessing i`
		s.k++    // want `mut is not held while accessing k`
	}
}

//lockguard:protected_by s.mut
func (s *S1) f1() {}

func funcLockUnlock() {
	var s1 S1
	s1.f1() // want `mut is not held while accessing f`

	s1.mut.Lock()
	s1.f1()
	s1.mut.Unlock()

	s1.f1() // want `mut is not held while accessing f`
}

func (s *S1) methodFuncLockUnlock() {
	s.f1() // want `mut is not held while accessing f`

	s.mut.Lock()
	s.f1()
	s.mut.Unlock()

	s.f1() // want `mut is not held while accessing f`
}

func funcLockDeferredUnlock() {
	var s1 S1
	s1.f1() // want `mut is not held while accessing f`

	s1.mut.Lock()
	defer s1.mut.Unlock()
	s1.f1()
}

func (s *S1) methodFuncLockUnlockDeferred() {
	s.f1() // want `mut is not held while accessing f`

	s.mut.Lock()
	defer s.mut.Unlock()
	s.f1()
}

//lockguard:protected_by s.mut
func (s *S1) f2() {
	// We can call fields protected by s.mut
	s.i++
	s.f1()
}

//lockguard:protected_by s.s1.mut
func (s *S2) f() {}

//lockguard:protected_by s.s1.mut
func (s *S2) f2() {
	s.s1.i++
	s.s1.f1()
	s.k++
}

func (s *S1) parenthesizedExpr() {
	((s).mut).Lock()
	defer ((s).mut).Unlock()

	((s).i)++
}
