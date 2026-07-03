package c

import "sync"

type channelTest struct {
	ch   chan int
	data int `protected_by:"mu"`
	mu   sync.Mutex
}

func (c *channelTest) selectWithProtected() {
	select {
	case c.data = <-c.ch: // want `writing 'data' requires holding 'mu'`
	case c.ch <- c.data: // want `reading 'data' requires holding 'mu'`
	}

	c.mu.Lock()
	select {
	case c.data = <-c.ch: // OK
	case c.ch <- c.data: // OK
	}
	c.mu.Unlock()
}

//import (
//	"fmt"
//	"sync"
//)
//
//type S1 struct {
//	i   int `protected_by:"mut"`
//	j   int
//	mut sync.Mutex
//}
//
//type conditionalLock struct {
//	data int `protected_by:"mu"`
//	mu   sync.Mutex
//}
//
//func (c *conditionalLock) deferConditionalLock(cond bool) {
//	func() {
//		if true {
//			fmt.Println("defering conditional lock")
//		}
//	}()
//
//	f := func() {
//		if true {
//			fmt.Println("defering conditional lock")
//		}
//	}
//	f()
//
//}
