package a

import "sync"

// ============================================================================
// Focused tests for conditional locking scenarios
// These specifically test the "possibly held" warnings
// ============================================================================

type conditionalLock struct {
	data int `protected_by:"mu"`
	mu   sync.Mutex
}

// ============================================================================
// Single if statement
// ============================================================================

func (c *conditionalLock) lockInIf(cond bool) {
	if cond {
		c.mu.Lock()
		c.data++ // OK - lock definitely held in this branch
		c.mu.Unlock()
	}
	// After if: lock is NOT held (all paths that acquired it released it)
	c.data++ // want `writing 'c\.data' requires holding 'c\.mu'`
}

func (c *conditionalLock) lockInIfNoUnlock(cond bool) {
	if cond {
		c.mu.Lock()
		c.data++ // OK
	}
	// After if: lock is POSSIBLY held (some paths hold it)
	c.data++ // want `writing 'c\.data' requires holding 'c\.mu' \(not held on all paths\)`
} // want `'c\.mu' possibly held at function exit \(possible lock leak\)`

func (c *conditionalLock) lockBeforeIf(cond bool) {
	c.mu.Lock()
	if cond {
		c.data++ // OK
	}
	c.data++ // OK
	c.mu.Unlock()
}

// ============================================================================
// If-else branches
// ============================================================================

func (c *conditionalLock) lockInBothBranches(cond bool) {
	if cond {
		c.mu.Lock()
	} else {
		c.mu.Lock()
	}
	// After if-else: lock is DEFINITELY held (all paths acquired it)
	c.data++ // OK

	c.mu.Unlock()
}

func (c *conditionalLock) lockInOneBranch(cond bool) {
	if cond {
		c.mu.Lock()
	} else {
		// No lock
	}
	// After if-else: lock is POSSIBLY held
	c.data++ // want `writing 'c\.data' requires holding 'c\.mu' \(not held on all paths\)`
} // want `'c\.mu' possibly held at function exit \(possible lock leak\)`

func (c *conditionalLock) lockInElseOnly(cond bool) {
	if cond {
		// No lock
	} else {
		c.mu.Lock()
	}
	// After if-else: lock is POSSIBLY held
	c.data++ // want `writing 'c\.data' requires holding 'c\.mu' \(not held on all paths\)`
} // want `'c\.mu' possibly held at function exit \(possible lock leak\)`

func (c *conditionalLock) unlockInOneBranch(cond bool) {
	c.mu.Lock()
	c.data++ // OK

	if cond {
		c.mu.Unlock()
	}
	// After if: lock is POSSIBLY held
	c.data++ // want `writing 'c\.data' requires holding 'c\.mu' \(not held on all paths\)`
} // want `'c\.mu' possibly held at function exit \(possible lock leak\)`

// ============================================================================
// If-else-if chains
// ============================================================================

func (c *conditionalLock) lockInSomeElseIf(a, b, cond bool) {
	if a {
		c.mu.Lock()
	} else if b {
		c.mu.Lock()
	} else if cond {
		// No lock
	}
	// Lock is POSSIBLY held (some but not all paths)
	c.data++ // want `writing 'c\.data' requires holding 'c\.mu' \(not held on all paths\)`
} // want `'c\.mu' possibly held at function exit \(possible lock leak\)`

func (c *conditionalLock) lockInAllElseIfPaths(a, b bool) {
	if a {
		c.mu.Lock()
	} else if b {
		c.mu.Lock()
	} else {
		c.mu.Lock()
	}
	// Lock is DEFINITELY held (all paths)
	c.data++ // OK

	c.mu.Unlock()
}

// ============================================================================
// Nested if statements
// ============================================================================

func (c *conditionalLock) nestedIfLockOuter(cond1, cond2 bool) {
	if cond1 {
		c.mu.Lock()
		if cond2 {
			c.data++ // OK
		}
		c.data++ // OK
		c.mu.Unlock()
	}
	c.data++ // want `writing 'c\.data' requires holding 'c\.mu'`
}

func (c *conditionalLock) nestedIfLockInner(cond1, cond2 bool) {
	if cond1 {
		if cond2 {
			c.mu.Lock()
			c.data++ // OK
			c.mu.Unlock()
		}
		c.data++ // want `writing 'c\.data' requires holding 'c\.mu'`
	}
	c.data++ // want `writing 'c\.data' requires holding 'c\.mu'`
}

func (c *conditionalLock) nestedIfBothLock(cond1, cond2 bool) {
	if cond1 {
		c.mu.Lock()
		if cond2 {
			c.data++ // OK
		} else {
			c.data++ // OK
		}
		c.mu.Unlock()
	}
	c.data++ // want `writing 'c\.data' requires holding 'c\.mu'`
}

// ============================================================================
// Switch statements with locks
// ============================================================================

func (c *conditionalLock) lockInSomeCase(v int) {
	switch v {
	case 1:
		c.mu.Lock()
		c.data++ // OK
		c.mu.Unlock()
	case 2:
		c.data++ // want `writing 'c\.data' requires holding 'c\.mu'`
	case 3:
		c.mu.Lock()
		c.data++ // OK
		c.mu.Unlock()
	}
	c.data++ // want `writing 'c\.data' requires holding 'c\.mu'`
}

func (c *conditionalLock) lockInOneCaseNoUnlock(v int) {
	switch v {
	case 1:
		c.mu.Lock()
		c.data++ // OK
	case 2:
		c.data++ // want `writing 'c\.data' requires holding 'c\.mu'`
	}
	// Lock is POSSIBLY held
	c.data++ // want `writing 'c\.data' requires holding 'c\.mu' \(not held on all paths\)`
} // want `'c\.mu' possibly held at function exit \(possible lock leak\)`

func (c *conditionalLock) lockInAllCases(v int) {
	switch v {
	case 1:
		c.mu.Lock()
	case 2:
		c.mu.Lock()
	default:
		c.mu.Lock()
	}
	// Lock is DEFINITELY held
	c.data++ // OK
	c.mu.Unlock()
}

func (c *conditionalLock) lockBeforeSwitch(v int) {
	c.mu.Lock()
	switch v {
	case 1:
		c.data++ // OK
	case 2:
		c.data++ // OK
	default:
		c.data++ // OK
	}
	c.mu.Unlock()
}

// ============================================================================
// For loops with conditional locking
// ============================================================================

func (c *conditionalLock) lockInsideLoop() {
	for i := 0; i < 10; i++ {
		c.mu.Lock()
		c.data++ // OK
		c.mu.Unlock()
	}
	c.data++ // want `writing 'c\.data' requires holding 'c\.mu'`
}

func (c *conditionalLock) lockOutsideLoop() {
	c.mu.Lock()
	for i := 0; i < 10; i++ {
		c.data++ // OK
	}
	c.mu.Unlock()
}

func (c *conditionalLock) lockInLoopCondition(cond bool) {
	for i := 0; i < 10; i++ {
		if cond {
			c.mu.Lock()
		}
		c.data++ // want `writing 'c\.data' requires holding 'c\.mu' \(not held on all paths\)`
		if cond {
			c.mu.Unlock() // want `releasing 'mu' that may not be held`
		}
	}
}

func (c *conditionalLock) breakWithLock() {
	c.mu.Lock()
	for i := 0; i < 10; i++ {
		if i == 5 {
			c.data++ // OK
			break
		}
		c.data++ // OK
	}
	c.data++ // OK
	c.mu.Unlock()
}

// ============================================================================
// Multiple locks with conditional acquisition
// ============================================================================

type twoLocks struct {
	a   int `protected_by:"mu1"`
	b   int `protected_by:"mu2"`
	mu1 sync.Mutex
	mu2 sync.Mutex
}

func (t *twoLocks) conditionalTwoLocks(cond1, cond2 bool) {
	if cond1 {
		t.mu1.Lock()
	}
	if cond2 {
		t.mu2.Lock()
	}

	// mu1 possibly held, mu2 possibly held
	t.a++ // want `writing 't\.a' requires holding 't\.mu1' \(not held on all paths\)`
	t.b++ // want `writing 't\.b' requires holding 't\.mu2' \(not held on all paths\)`

	if cond1 {
		t.mu1.Unlock() // want `releasing 'mu1' that may not be held`
	}
	if cond2 {
		t.mu2.Unlock() // want `releasing 'mu2' that may not be held`
	}
} // want `'t\.mu1' possibly held at function exit \(possible lock leak\)` `'t\.mu2' possibly held at function exit \(possible lock leak\)`

// ============================================================================
// Early returns with conditional locks
// ============================================================================

func (c *conditionalLock) earlyReturnConditional(cond1, cond2 bool) {
	if cond1 {
		c.mu.Lock()
		c.data++ // OK
		c.mu.Unlock()
		return
	}

	if cond2 {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.data++ // OK
		return
	}

	c.data++ // want `writing 'c\.data' requires holding 'c\.mu'`
}

func (c *conditionalLock) lockBeforeEarlyReturn(cond bool) {
	c.mu.Lock()

	if cond {
		c.data++ // OK
		c.mu.Unlock()
		return
	}

	c.data++ // OK
	c.mu.Unlock()
}

// ============================================================================
// Defer with conditional locking
// ============================================================================

func (c *conditionalLock) deferConditionalLock(cond bool) {
	if cond {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.data++ // OK
	}

	c.data++ // want `writing 'c\.data' requires holding 'c\.mu' \(not held on all paths\)`
}

func (c *conditionalLock) conditionalDefer(cond bool) {
	c.mu.Lock()
	if cond {
		defer c.mu.Unlock()
	}
	c.data++ // OK (lock held now)

	// At end of function: lock POSSIBLY held
	// (released by defer only if cond was true)
}

// ============================================================================
// Complex control flow combinations
// ============================================================================

func (c *conditionalLock) complexFlow(a, b, cond bool) {
	if a {
		c.mu.Lock()
		if b {
			c.data++ // OK
		}
	} else {
		if cond {
			c.mu.Lock()
		}
	}

	// Lock is POSSIBLY held (some paths acquire it)
	c.data++ // want `writing 'c\.data' requires holding 'c\.mu' \(not held on all paths\)`
} // want `'c\.mu' possibly held at function exit \(possible lock leak\)`

func (c *conditionalLock) switchInIf(v int, cond bool) {
	if cond {
		switch v {
		case 1:
			c.mu.Lock()
		case 2:
			c.mu.Lock()
		default:
			c.mu.Lock()
		}
		// Within if branch: lock definitely held
		c.data++ // OK
		c.mu.Unlock()
	}

	// Outside if: lock not held
	c.data++ // want `writing 'c\.data' requires holding 'c\.mu'`
}

// ============================================================================
// Loops with early exits
// ============================================================================

func (c *conditionalLock) loopWithConditionalBreak(cond bool) {
	c.mu.Lock()
	for i := 0; i < 10; i++ {
		c.data++ // OK
		if cond {
			c.mu.Unlock()
			break
		}
	}
	// Lock is POSSIBLY held (released if break was taken)
	c.data++ // want `writing 'c\.data' requires holding 'c\.mu' \(not held on all paths\)`
} // want `'c\.mu' possibly held at function exit \(possible lock leak\)`

func (c *conditionalLock) loopWithConditionalContinue(cond bool) {
	for i := 0; i < 10; i++ {
		if cond {
			c.mu.Lock()
			c.data++ // OK
			continue
		}
		c.data++ // want `writing 'c\.data' requires holding 'c\.mu'`
	}
}

// ============================================================================
// Select with locks
// ============================================================================

type selectLock struct {
	ch1  chan int
	ch2  chan int
	data int `protected_by:"mu"`
	mu   sync.Mutex
}

func (s *selectLock) selectWithLockInCase() {
	select {
	case <-s.ch1:
		s.mu.Lock()
		s.data++ // OK
		s.mu.Unlock()
	case <-s.ch2:
		s.data++ // want `writing 's\.data' requires holding 's\.mu'`
	}

	s.data++ // want `writing 's\.data' requires holding 's\.mu'`
}

func (s *selectLock) selectWithLockInAllCases() {
	select {
	case <-s.ch1:
		s.mu.Lock()
		s.data++ // OK
	case <-s.ch2:
		s.mu.Lock()
		s.data++ // OK
	default:
		s.mu.Lock()
		s.data++ // OK
	}

	// Lock definitely held (all cases acquired it)
	s.data++ // OK
	s.mu.Unlock()
}

// ============================================================================
// Boolean flag pattern (common but fragile)
// ============================================================================

func (c *conditionalLock) flagPattern(cond bool) {
	locked := false
	if cond {
		c.mu.Lock()
		locked = true
	}

	if locked {
		c.data++ // want `writing 'c\.data' requires holding 'c\.mu' \(not held on all paths\)`
		// Tool can't track the boolean flag correlation
	}

	if locked {
		c.mu.Unlock() // want `releasing 'mu' that may not be held`
	}
} // want `'c\.mu' possibly held at function exit \(possible lock leak\)`
