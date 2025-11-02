package a

import "sync"

type multiLock struct {
	a   int `protected_by:"mu1"`
	b   int `protected_by:"mu2"`
	c   int `protected_by:"mu1"`
	d   int // unprotected
	mu1 sync.Mutex
	mu2 sync.Mutex
}

func (m *multiLock) wrongLock() {
	m.mu1.Lock()
	m.a++ // OK
	m.b++ // want `mu2 is not held while accessing b`
	m.c++ // OK
	m.mu1.Unlock()
}

func (m *multiLock) bothLocks() {
	m.mu1.Lock()
	m.mu2.Lock()
	m.a++ // OK
	m.b++ // OK
	m.c++ // OK
	m.mu2.Unlock()
	m.mu1.Unlock()
}

func (m *multiLock) lockOrdering() {
	m.mu1.Lock()
	m.a++ // OK
	m.mu1.Unlock()

	m.mu2.Lock()
	m.b++ // OK
	m.mu2.Unlock()

	m.a++ // want `mu1 is not held while accessing a`
}
