package a

import "fmt"

type S1 struct {
	i int `protected_by:"mu"`
}

type S2 = struct {
	i int `protected_by:"mu"`
}

// Func This is a function
func Func() {
	type S3 struct {
		i string `protected_by:"mu"`
	}

	v := struct {
		i int `protected_by:"mu"`
	}{i: 1}
	fmt.Println(v)
}
