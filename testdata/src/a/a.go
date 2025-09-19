package a

import "sync"

type S1 struct {
	i   int `protected_by:"mut"`
	mut sync.Mutex
}

type AS1 = S1

type S2 struct {
	s1 AS1
	i  int `protected_by:"s1.mut"`
}

type AS2 = S2

type AS2_1 = AS2

type S3 struct {
	s2 AS2_1
	i  int `protected_by:"s2.s1.mut"`
}

// Func This is a function
func Func() {

}
