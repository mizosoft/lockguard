package a

import "sync"

type rwLockHolder struct {
	mu sync.RWMutex
}

func (d *rwLockHolder) deadlockByLockingMultipleTimes() {
	d.mu.Lock()
	d.mu.Lock()  // want `acquiring 'mu' that is already held \[deadlock\]`
	d.mu.RLock() // want `acquiring 'mu' that is already held \[deadlock\]`
	d.mu.RUnlock()
	d.mu.Unlock()
	d.mu.Unlock()

	// RLocking/RUnlocking multiple times is fine.
	d.mu.RLock()
	d.mu.RLock()
	d.mu.Lock() // want `acquiring 'mu' that is already held \[deadlock\]`
	d.mu.Unlock()
	d.mu.RUnlock()
	d.mu.RUnlock()
}

func (d *rwLockHolder) misalignedLocking() {
	d.mu.RUnlock() // want `releasing read lock on 'mu' that is not held`
	d.mu.Unlock()  // want `releasing 'mu' that is not held`

	d.mu.Lock()
	d.mu.RUnlock() // want `releasing read lock on 'mu' that is not held`
	d.mu.Unlock()

	d.mu.RLock()
	d.mu.Unlock() // want `releasing 'mu' that is not held`
	d.mu.RUnlock()
}
