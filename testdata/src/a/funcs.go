package a

import "sync"

type methodProtection struct {
	a   int `protected_by:"mu1"`
	b   int `protected_by:"mu2"`
	mu1 sync.Mutex
	mu2 sync.Mutex
}

//lockguard:protected_by m.mu1
func (m *methodProtection) requiresMu1() {
	m.a++ // OK (mu1 is held by caller requirement)
	m.b++ // want `mu2 is not held while accessing b`
}

//lockguard:protected_by m.mu2
func (m *methodProtection) requiresMu2() {
	m.a++ // want `mu1 is not held while accessing a`
	m.b++ // OK
}

//lockguard:protected_by m.mu1
//lockguard:protected_by m.mu2
func (m *methodProtection) requiresBoth() {
	m.a++ // OK
	m.b++ // OK
}

func (m *methodProtection) callProtectedMethods() {
	m.requiresMu1() // want `mu1 is not held while accessing requiresMu1`

	m.mu1.Lock()
	m.requiresMu1() // OK
	m.requiresMu2() // want `mu2 is not held while accessing requiresMu2`
	m.mu1.Unlock()

	m.mu1.Lock()
	m.mu2.Lock()
	m.requiresBoth() // OK
	m.mu2.Unlock()
	m.mu1.Unlock()
}
