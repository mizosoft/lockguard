package a

import (
	"fmt"
	"sync"
)

type S1 struct {
	i  int `protected_by:"mu"`
	mu sync.Mutex
}

type S2 = struct {
	i  int `protected_by:"mu"`
	mu sync.Mutex
}

type S3 = struct {
	i, j, k int `protected_by:"mu"`
	mu      sync.Mutex
}

// Func This is a function
func Func() {
	type S3 struct {
		i  string `protected_by:"mu"`
		mu sync.Mutex
	}

	v := struct {
		i int `protected_by:"mu"`
	}{i: 1}
	fmt.Println(v)

	s1 := S1{i: 1}
	fmt.Println(s1.i)

	fmt.Println(S2{i: 1}.i)

	fmt.Println(S3{i: ""}.i)
}
