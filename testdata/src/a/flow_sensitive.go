//go:build linux
// +build linux

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
	f.x++ // want `writing 'x' requires holding 'mu' \(not held on all paths\)`
}

func (f *flowSensitive) multipleConditionalPaths(a, b bool) {
	if a {
		f.mu.Lock()
	} else if b {
		f.mu.Lock()
	}
	// Lock is possibly held (acquired in some paths but not all)
	f.x++ // want `writing 'x' requires holding 'mu' \(not held on all paths\)`

	if a || b {
		f.mu.Unlock() // want `releasing 'mu' that may not be held`
	}
}

func (f *flowSensitive) lockInOneUnlockInAnother(cond bool) {
	if cond {
		f.mu.Lock()
	}

	if !cond {
		f.mu.Lock()
	}

	// At this point, lock is ALWAYS held (both paths acquire)
	f.x++ // OK

	f.mu.Unlock()
}

func (f *flowSensitive) inconsistentUnlock(cond bool) {
	f.mu.Lock()
	f.x++ // OK

	if cond {
		f.mu.Unlock()
	}

	// Lock is possibly held here
	f.x++ // want `writing 'x' requires holding 'mu' \(not held on all paths\)`
}

func (f *flowSensitive) lockAfterBranch(cond bool) {
	var locked bool
	if cond {
		f.mu.Lock()
		locked = true
	}

	if locked {
		f.x++         // want `writing 'x' requires holding 'mu' \(not held on all paths\)`
		f.mu.Unlock() // want `releasing 'mu' that may not be held`
	}
}

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
	g.x++ // want `writing 'x' requires holding 'mu'`
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

	g.x++ // want `writing 'x' requires holding 'mu'`
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
		c.state++ // want `writing 'state' requires holding 'mu'`
	case 3:
		c.mu.Lock()
		c.state++ // OK
	}

	// Lock is possibly held (acquired in some cases)
	c.state++ // want `writing 'state' requires holding 'mu' \(not held on all paths\)`
}

func (c *complexSwitch) switchWithFallthrough(mode int) {
	switch mode {
	case 1:
		c.mu.Lock()
		fallthrough
	case 2:
		c.state++     // want `writing 'state' requires holding 'mu' \(not held on all paths\)`
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
	c.ch <- c.data // want `reading 'data' requires holding 'mu'`

	c.mu.Lock()
	c.ch <- c.data // OK
	c.mu.Unlock()
}

func (c *channelTest) receiveToProtected() {
	c.data = <-c.ch // want `writing 'data' requires holding 'mu'`

	c.mu.Lock()
	c.data = <-c.ch // OK
	c.mu.Unlock()
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

// ============================================================================
// Map and slice with protected elements
// ============================================================================

type collectionTest struct {
	mapData   map[string]int `protected_by:"mu"`
	sliceData []int          `protected_by:"mu"`
	mu        sync.Mutex
}

func (c *collectionTest) mapAccess() {
	c.mapData["key"] = 42            // want `writing 'mapData' requires holding 'mu'`
	_ = c.mapData["key"]             // want `reading 'mapData' requires holding 'mu'`
	delete(c.mapData, "key")         // want `reading 'mapData' requires holding 'mu'`
	c.mapData = make(map[string]int) // want `writing 'mapData' requires holding 'mu'`

	c.mu.Lock()
	c.mapData["key"] = 42    // OK
	_ = c.mapData["key"]     // OK
	delete(c.mapData, "key") // OK
	c.mu.Unlock()
}

func (c *collectionTest) sliceAccess() {
	c.sliceData[0] = 42                  // want `writing 'sliceData' requires holding 'mu'`
	_ = c.sliceData[0]                   // want `reading 'sliceData' requires holding 'mu'`
	c.sliceData = append(c.sliceData, 1) // want `writing 'sliceData' requires holding 'mu'`
	_ = len(c.sliceData)                 // want `reading 'sliceData' requires holding 'mu'`

	c.mu.Lock()
	c.sliceData[0] = 42                  // OK
	_ = c.sliceData[0]                   // OK
	c.sliceData = append(c.sliceData, 1) // OK
	c.mu.Unlock()
}

func (c *collectionTest) rangeOverProtected() {
	for k, v := range c.mapData { // want `reading 'mapData' requires holding 'mu'`
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
		m.x++ // want `writing 'x' requires holding 'mu'`
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
}

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

func (l *lockAsValue) lockViaReturn() {
	mu := l.getMutex()
	mu.Lock()
	l.x++ // OK
	mu.Unlock()
}

func helperLock(mu *sync.Mutex) {
	mu.Lock()
}

func helperUnlock(mu *sync.Mutex) {
	mu.Unlock()
}

func (l *lockAsValue) lockViaHelper() {
	helperLock(&l.mu)
	l.x++ // OK (but tool may not track this)
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
	_ = o.inner.getValue() // want `reading 'inner' requires holding 'mu'`

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
	p.count++ // want `writing 'count' requires holding 'mu'`
}

func (p *protectedCounter) Value() int {
	return p.count // want `reading 'count' requires holding 'mu'`
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

	arr[0].x++ // want `writing 'x' requires holding 'mu'`

	arr[0].mu.Lock()
	arr[0].x++ // OK
	arr[0].mu.Unlock()
}

func sliceOfProtected() {
	arr := make([]arrayElement, 5)

	arr[0].x++ // want `writing 'x' requires holding 'mu'`

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
	for l.counter < 10 { // want `reading 'counter' requires holding 'mu'`
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
// Conditional compilation / build tags (if supported)
// ============================================================================

type platformSpecific struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

func (p *platformSpecific) linuxOnly() {
	p.x++ // want `writing 'x' requires holding 'mu'`
}

// ============================================================================
// Anonymous struct fields
// ============================================================================

func anonymousStructs() {
	s := struct {
		x  int `protected_by:"mu"`
		mu sync.Mutex
	}{}

	s.x++ // want `writing 'x' requires holding 'mu'`

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

func (c *crossBoundary) lockHere() {
	c.mu.Lock()
	c.x++ // OK
}

func (c *crossBoundary) unlockThere() {
	c.x++ // Tool doesn't know lock is held from lockHere
	c.mu.Unlock()
}

func (c *crossBoundary) dangerousPattern() {
	c.lockHere()
	// Lock is held here but tool doesn't track across calls
	c.unlockThere()
}
