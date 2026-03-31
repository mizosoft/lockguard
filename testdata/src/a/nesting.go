package a

import "sync"

type level3 struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

type level2 struct {
	l3 level3
	y  int `protected_by:"l3.mu"`
}

type level1 struct {
	l2 level2
	z  int `protected_by:"l2.l3.mu"`
}

func (l *level1) deepNesting() {
	l.z++       // want `writing 'l\.z' requires holding 'l\.l2\.l3\.mu'`
	l.l2.y++    // want `writing 'l\.l2\.y' requires holding 'l\.l2\.l3\.mu'`
	l.l2.l3.x++ // want `writing 'l\.l2\.l3\.x' requires holding 'l\.l2\.l3\.mu'`

	l.l2.l3.mu.Lock()
	l.z++       // OK
	l.l2.y++    // OK
	l.l2.l3.x++ // OK
	l.l2.l3.mu.Unlock()
}
