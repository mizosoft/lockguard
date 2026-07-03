package a

import "sync"

// A type that implements sync.Locker (or the RW variant) has Lock/Unlock methods whose whole job is
// to acquire or release a lock across the call boundary. Analyzed in isolation, Lock() looks like a
// lock leak and Unlock() like a release of a lock that is not held. lockguard grants such methods a
// one-shot allowance so these expected imbalances are not reported.

// customMutex implements sync.Locker.
type customMutex struct {
	mu sync.Mutex
}

func (c *customMutex) Lock() {
	c.mu.Lock() // OK: a locking method is allowed to leave the lock held at exit.
}

func (c *customMutex) Unlock() {
	c.mu.Unlock() // OK: an unlocking method is allowed to release a lock taken by its caller.
}

// TryLock conditionally acquires the lock and holds it on the success path (as sync.Mutex.TryLock
// does). The lock is held at the true-return, which is allowed for a locking method.
func (c *customMutex) TryLock() bool {
	if !c.mu.TryLock() {
		return false
	}
	return true // OK: lock held at exit on success.
}

// customRWMutex implements sync.Locker and the RLock/RUnlock pair, so it is treated as an RWLocker.
type customRWMutex struct {
	rw sync.RWMutex
}

func (c *customRWMutex) Lock()    { c.rw.Lock() }    // OK
func (c *customRWMutex) Unlock()  { c.rw.Unlock() }  // OK
func (c *customRWMutex) RLock()   { c.rw.RLock() }   // OK
func (c *customRWMutex) RUnlock() { c.rw.RUnlock() } // OK

// notALocker has only a Lock method, so it does NOT implement sync.Locker and gets no allowance:
// the leak is a real one and must still be reported.
type notALocker struct {
	mu sync.Mutex
}

func (n *notALocker) Lock() {
	n.mu.Lock() // want `'n\.mu' acquired but never unlocked`
}