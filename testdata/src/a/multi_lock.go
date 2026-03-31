package a

import "sync"

type withMultipleLocks struct {
	a   int `protected_by:"mu1"`
	b   int `protected_by:"mu2"`
	c   int `protected_by:"mu1"`
	d   int // unprotected
	mu1 sync.Mutex
	mu2 sync.Mutex
}

func (m *withMultipleLocks) wrongLock() {
	m.mu1.Lock()
	m.a++ // OK
	m.b++ // want `writing 'm\.b' requires holding 'm\.mu2'`
	m.c++ // OK
	m.mu1.Unlock()
}

func (m *withMultipleLocks) bothLocks() {
	m.mu1.Lock()
	m.mu2.Lock()
	m.a++ // OK
	m.b++ // OK
	m.c++ // OK
	m.mu2.Unlock()
	m.mu1.Unlock()
}

func (m *withMultipleLocks) lockOrdering() {
	m.mu1.Lock()
	m.a++ // OK
	m.mu1.Unlock()

	m.mu2.Lock()
	m.b++ // OK
	m.mu2.Unlock()

	m.a++ // want `writing 'm\.a' requires holding 'm\.mu1'`
}
