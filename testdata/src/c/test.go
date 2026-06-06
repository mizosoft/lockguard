package c

import (
	"fmt"
	"sync"
)

type S1 struct {
	i   int `protected_by:"mut"`
	j   int
	mut sync.Mutex
}

type conditionalLock struct {
	data int `protected_by:"mu"`
	mu   sync.Mutex
}

func (c *conditionalLock) deferConditionalLock(cond bool) {
	func() {
		if true {
			fmt.Println("defering conditional lock")
		}
	}()

	f := func() {
		if true {
			fmt.Println("defering conditional lock")
		}
	}
	f()

}
