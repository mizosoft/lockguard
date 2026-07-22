package a

import "sync"

// ============================================================================
// Flow-sensitive analysis: locks acquired conditionally
// ============================================================================

type flowSensitive struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

func (f *flowSensitive) conditionalLockAcquire(needsLock bool) {
	if needsLock {
		f.mu.Lock()
		defer f.mu.Unlock()
	}
	// After the if, lock is POSSIBLY held
	f.x++ // want `writing 'f\.x' requires holding 'f\.mu' \(not held on all paths\)`
}

func (f *flowSensitive) multipleConditionalPaths(a, b bool) {
	if a {
		f.mu.Lock()
	} else if b {
		f.mu.Lock()
	}
	// Lock is possibly held (acquired in some paths but not all)
	f.x++ // want `writing 'f\.x' requires holding 'f\.mu' \(not held on all paths\)`

	if a || b {
		f.mu.Unlock() // want `releasing 'mu' that may not be held`
	}
} // want `'f\.mu' may not be unlocked at function exit`

func (f *flowSensitive) lockInOneUnlockInAnother(cond bool) {
	if cond {
		f.mu.Lock()
	}

	if !cond {
		f.mu.Lock() // want `acquiring 'mu' may cause deadlock: may already be held`
	}

	// At this point, lock is ALWAYS held (both paths acquire)
	f.x++ // want `writing 'f\.x' requires holding 'f\.mu' \(not held on all paths\)`

	f.mu.Unlock() // want `releasing 'mu' that may not be held`
} // want `'f\.mu' may not be unlocked at function exit`

func (f *flowSensitive) inconsistentUnlock(cond bool) {
	f.mu.Lock()
	f.x++ // OK

	if cond {
		f.mu.Unlock()
	}

	// Lock is possibly held here
	f.x++ // want `writing 'f\.x' requires holding 'f\.mu' \(not held on all paths\)`
} // want `'f\.mu' may not be unlocked at function exit`

func (f *flowSensitive) lockAfterBranch(cond bool) {
	var locked bool
	if cond {
		f.mu.Lock()
		locked = true
	}

	if locked {
		f.x++         // want `writing 'f\.x' requires holding 'f\.mu' \(not held on all paths\)`
		f.mu.Unlock() // want `releasing 'mu' that may not be held`
	}
} // want `'f\.mu' may not be unlocked at function exit`

// ============================================================================
// Goto statements
// ============================================================================

type gotoTest struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

func (g *gotoTest) gotoSkipsLock() {
	goto skip
	g.mu.Lock() // This is skipped

skip:
	g.x++ // want `writing 'g\.x' requires holding 'g\.mu'`
}

func (g *gotoTest) gotoToLocked() {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.x > 10 { // OK
		goto done
	}

	g.x++ // OK

done:
	g.x++ // OK (lock still held due to defer)
}

func (g *gotoTest) gotoWithinLock() {
	g.mu.Lock()

	if g.x > 5 { // OK
		goto unlock
	}

	g.x++ // OK

unlock:
	g.mu.Unlock()

	g.x++ // want `writing 'g\.x' requires holding 'g\.mu'`
}

// ============================================================================
// Panic and recover
// ============================================================================

type panicTest struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

func (p *panicTest) panicWhileLocked() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.x++ // OK

	if p.x > 100 { // OK
		panic("overflow")
	}

	p.x++ // OK
}

func (p *panicTest) recoverPattern() {
	p.mu.Lock()
	defer func() {
		if r := recover(); r != nil {
			p.x++ // OK (lock still held in defer)
		}
		p.mu.Unlock()
	}()

	p.x++ // OK
	panic("test")
}

// ============================================================================
// Complex switch with locks in different cases
// ============================================================================

type complexSwitch struct {
	state int `protected_by:"mu"`
	mu    sync.Mutex
}

func (c *complexSwitch) switchLockInCase(mode int) {
	switch mode {
	case 1:
		c.mu.Lock()
		c.state++ // OK
	case 2:
		c.state++ // want `writing 'c\.state' requires holding 'c\.mu'`
	case 3:
		c.mu.Lock()
		c.state++ // OK
	}

	// Lock is possibly held (acquired in some cases)
	c.state++ // want `writing 'c\.state' requires holding 'c\.mu' \(not held on all paths\)`
} // want `'c\.mu' may not be unlocked at function exit`

func (c *complexSwitch) switchWithFallthrough(mode int) {
	switch mode {
	case 1:
		c.mu.Lock()
		fallthrough
	case 2:
		c.state++     // want `writing 'c\.state' requires holding 'c\.mu' \(not held on all paths\)`
		c.mu.Unlock() // want `releasing 'mu' that may not be held`
	}
}

// ============================================================================
// Channel operations with protected data
// ============================================================================

type channelTest struct {
	ch   chan int
	data int `protected_by:"mu"`
	mu   sync.Mutex
}

func (c *channelTest) sendProtectedData() {
	c.ch <- c.data // want `reading 'c\.data' requires holding 'c\.mu'`

	c.mu.Lock()
	c.ch <- c.data // OK
	c.mu.Unlock()
}

func (c *channelTest) receiveToProtected() {
	c.data = <-c.ch // want `writing 'c\.data' requires holding 'c\.mu'`

	c.mu.Lock()
	c.data = <-c.ch // OK
	c.mu.Unlock()
}

func (c *channelTest) selectWithProtected() {
	select {
	case c.data = <-c.ch: // want `writing 'c\.data' requires holding 'c\.mu'`
	case c.ch <- c.data: // want `reading 'c\.data' requires holding 'c\.mu'`
	}

	c.mu.Lock()
	select {
	case c.data = <-c.ch: // OK
	case c.ch <- c.data: // OK
	}
	c.mu.Unlock()
}

// ============================================================================
// Map and slice with protected elements
// ============================================================================

type collectionTest struct {
	mapData   map[string]int `protected_by:"mu"`
	sliceData []int          `protected_by:"mu"`
	mu        sync.Mutex
}

func (c *collectionTest) mapAccess() {
	c.mapData["key"] = 42            // want `writing 'c\.mapData' requires holding 'c\.mu'`
	_ = c.mapData["key"]             // want `reading 'c\.mapData' requires holding 'c\.mu'`
	delete(c.mapData, "key")         // want `reading 'c\.mapData' requires holding 'c\.mu'`
	c.mapData = make(map[string]int) // want `writing 'c\.mapData' requires holding 'c\.mu'`

	c.mu.Lock()
	c.mapData["key"] = 42    // OK
	_ = c.mapData["key"]     // OK
	delete(c.mapData, "key") // OK
	c.mu.Unlock()
}

func (c *collectionTest) sliceAccess() {
	c.sliceData[0] = 42                  // want `writing 'c\.sliceData' requires holding 'c\.mu'`
	_ = c.sliceData[0]                   // want `reading 'c\.sliceData' requires holding 'c\.mu'`
	c.sliceData = append(c.sliceData, 1) // want `writing 'c\.sliceData' requires holding 'c\.mu'` `reading 'c\.sliceData' requires holding 'c\.mu'`
	_ = len(c.sliceData)                 // want `reading 'c\.sliceData' requires holding 'c\.mu'`

	c.mu.Lock()
	c.sliceData[0] = 42                  // OK
	_ = c.sliceData[0]                   // OK
	c.sliceData = append(c.sliceData, 1) // OK
	c.mu.Unlock()
}

func (c *collectionTest) rangeOverProtected() {
	for k, v := range c.mapData { // want `reading 'c\.mapData' requires holding 'c\.mu'`
		_ = k
		_ = v
	}

	c.mu.Lock()
	for k, v := range c.mapData { // OK
		_ = k
		_ = v
	}
	c.mu.Unlock()
}

// ============================================================================
// Multiple early returns with different lock states
// ============================================================================

type multiReturn struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

func (m *multiReturn) multipleReturns(a, b bool) {
	if a {
		m.mu.Lock()
		m.x++ // OK
		m.mu.Unlock()
		return
	}

	if b {
		m.x++ // want `writing 'm\.x' requires holding 'm\.mu'`
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.x++ // OK
}

func (m *multiReturn) earlyReturnSkipsUnlock(cond bool) {
	m.mu.Lock()

	if cond {
		m.x++  // OK
		return // Lock never unlocked on this path (potential deadlock)
	}

	m.x++ // OK
	m.mu.Unlock()
} // want `'m\.mu' may not be unlocked at function exit`

// ============================================================================
// Lock passed as argument or returned
// ============================================================================

type lockAsValue struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

func (l *lockAsValue) getMutex() *sync.Mutex {
	return &l.mu
}

// The following exercise locks reached through a returned pointer (lockViaReturn) or passed to a
// helper (helperLock / helperUnlock / lockViaHelper). lockguard tracks neither pointer aliasing nor
// lock state across function calls, so it cannot tell that mu / &l.mu refers to l.mu. The
// diagnostics asserted below are therefore FALSE POSITIVES; the assertions pin current behavior,
// they do not endorse it.
// TODO(limitation): track pointer aliasing and interprocedural lock state so these stop firing.
// See TODO.md ("Pointer aliasing", "No inter-procedural analysis").
func (l *lockAsValue) lockViaReturn() {
	mu := l.getMutex()
	mu.Lock()
	l.x++ // want `writing 'l\.x' requires holding 'l\.mu'`
	mu.Unlock()
}

func helperLock(mu *sync.Mutex) {
	mu.Lock() // want `'mu' acquired but never unlocked`
}

func helperUnlock(mu *sync.Mutex) {
	mu.Unlock() // want `releasing 'mu' that is not held`
}

func (l *lockAsValue) lockViaHelper() {
	helperLock(&l.mu)
	l.x++ // want `writing 'l\.x' requires holding 'l\.mu'`
	helperUnlock(&l.mu)
}

// ============================================================================
// Nested loops with different lock scopes
// ============================================================================

type nestedLoops struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

func (n *nestedLoops) nestedLoopLockOuter() {
	n.mu.Lock()
	for i := 0; i < 10; i++ {
		for j := 0; j < 10; j++ {
			n.x++ // OK
		}
	}
	n.mu.Unlock()
}

func (n *nestedLoops) nestedLoopLockInner() {
	for i := 0; i < 10; i++ {
		for j := 0; j < 10; j++ {
			n.mu.Lock()
			n.x++ // OK
			n.mu.Unlock()
		}
	}
}

func (n *nestedLoops) breakContinueWithLock() {
	n.mu.Lock()
	for i := 0; i < 10; i++ {
		if i == 5 {
			n.x++ // OK
			break
		}
		if i == 3 {
			n.x++ // OK
			continue
		}
		n.x++ // OK
	}
	n.mu.Unlock()
}

// ============================================================================
// Struct fields that are themselves structs with methods
// ============================================================================

type innerStruct struct {
	value int
	mu    sync.Mutex
}

func (i *innerStruct) getValue() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.value
}

type outerStruct struct {
	inner innerStruct `protected_by:"mu"`
	mu    sync.Mutex
}

func (o *outerStruct) callMethodOnProtected() {
	_ = o.inner.getValue() // want `reading 'o\.inner' requires holding 'o\.mu'`

	o.mu.Lock()
	_ = o.inner.getValue() // OK
	o.mu.Unlock()
}

// ============================================================================
// Interface with protected implementation
// ============================================================================

type Counter interface {
	Increment()
	Value() int
}

type protectedCounter struct {
	count int `protected_by:"mu"`
	mu    sync.Mutex
}

func (p *protectedCounter) Increment() {
	p.count++ // want `writing 'p\.count' requires holding 'p\.mu'`
}

func (p *protectedCounter) Value() int {
	return p.count // want `reading 'p\.count' requires holding 'p\.mu'`
}

func (p *protectedCounter) CorrectIncrement() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.count++ // OK
}

// ============================================================================
// Array of protected structs
// ============================================================================

type arrayElement struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

func arrayOfProtected() {
	var arr [5]arrayElement

	// TODO(limitation): index-expression bases (arr[0].x) are not resolved, so this unprotected
	// write is NOT flagged though it should be. See TODO.md ("Index expressions").
	arr[0].x++ // not flagged (limitation)

	arr[0].mu.Lock()
	arr[0].x++ // OK
	arr[0].mu.Unlock()
}

func sliceOfProtected() {
	arr := make([]arrayElement, 5)

	// TODO(limitation): see arrayOfProtected — index-expression bases are not resolved, so this
	// unprotected write is NOT flagged. See TODO.md ("Index expressions").
	arr[0].x++ // not flagged (limitation)

	arr[0].mu.Lock()
	arr[0].x++ // OK
	arr[0].mu.Unlock()
}

// ============================================================================
// Lock in loop condition
// ============================================================================

type loopCondition struct {
	counter int `protected_by:"mu"`
	mu      sync.Mutex
}

func (l *loopCondition) lockInCondition() {
	// Lock check in condition without holding lock
	for l.counter < 10 { // want `reading 'l\.counter' requires holding 'l\.mu'`
		l.mu.Lock()
		l.counter++ // OK
		l.mu.Unlock()
	}
}

func (l *loopCondition) lockBeforeLoop() {
	l.mu.Lock()
	defer l.mu.Unlock()

	for l.counter < 10 { // OK
		l.counter++ // OK
	}
}

// ============================================================================
// Anonymous struct fields
// ============================================================================

func anonymousStructs() {
	s := struct {
		x  int `protected_by:"mu"`
		mu sync.Mutex
	}{}

	s.x++ // want `writing 's\.x' requires holding 's\.mu'`

	s.mu.Lock()
	s.x++ // OK
	s.mu.Unlock()
}

// ============================================================================
// Lock held across function boundary
// ============================================================================

type crossBoundary struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

// lockHere / unlockThere split a lock acquire and release across two methods. With no
// interprocedural analysis, lockguard sees lockHere leak the lock and unlockThere release a lock it
// never took — both FALSE POSITIVES for this (unusual) cross-boundary pattern, pinned here.
// TODO(limitation): interprocedural lock state. See TODO.md ("No inter-procedural analysis").
func (c *crossBoundary) lockHere() {
	c.mu.Lock() // want `'c\.mu' acquired but never unlocked`
	c.x++       // OK
}

func (c *crossBoundary) unlockThere() {
	c.x++         // want `writing 'c\.x' requires holding 'c\.mu'`
	c.mu.Unlock() // want `releasing 'mu' that is not held`
}

func (c *crossBoundary) dangerousPattern() {
	c.lockHere()
	// Lock is held here but tool doesn't track across calls
	c.unlockThere()
}
