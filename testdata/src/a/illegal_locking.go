package a

import "sync"

type rwLockHolder struct {
	mu sync.RWMutex
}

func (d *rwLockHolder) deadlockByLockingMultipleTimes() {
	d.mu.Lock()
	d.mu.Lock()  // want `deadlock: mu - already locked`
	d.mu.RLock() // want `deadlock: mu - already locked`
	d.mu.RUnlock()
	d.mu.Unlock()
	d.mu.Unlock()

	// RLocking/RUnlocking multiple times is fine.
	d.mu.RLock()
	d.mu.RLock()
	d.mu.Lock() // want `deadlock: mu - already locked`
	d.mu.Unlock()
	d.mu.RUnlock()
	d.mu.RUnlock()
}

func (d *rwLockHolder) misalignedLocking() {
	d.mu.RUnlock() // want `mu - read-unlocking a non-locked lock`
	d.mu.Unlock()  // want `mu - unlocking a non-locked lock`

	d.mu.Lock()
	d.mu.RUnlock() // want `mu - read-unlocking a non-locked lock`
	d.mu.Unlock()

	d.mu.RLock()
	d.mu.Unlock() // want `mu - unlocking a non-locked lock`
	d.mu.RUnlock()
}
