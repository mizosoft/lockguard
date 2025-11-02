package a

import "sync"

type controlFlow struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

func (c *controlFlow) ifElseBranches(cond bool) {
	if cond {
		c.mu.Lock()
		c.x++ // OK
		c.mu.Unlock()
	} else {
		c.x++ // want `mu is not held while accessing x`
	}

	c.x++ // want `mu is not held while accessing x`
}

func (c *controlFlow) lockInBranch(cond bool) {
	if cond {
		c.mu.Lock()
	}
	c.x++ // want `mu is not held while accessing x`
	if cond {
		c.mu.Unlock()
	}
}

func (c *controlFlow) lockBeforeBranch(cond bool) {
	c.mu.Lock()
	if cond {
		c.x++ // OK
	} else {
		c.x++ // OK
	}
	c.mu.Unlock()
}

func (c *controlFlow) earlyReturn() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.x > 10 { // OK (reading under lock)
		return
	}
	c.x++ // OK
}

// ============================================================================
// Switch statements
// ============================================================================

type switchTest struct {
	state int `protected_by:"mu"`
	mu    sync.Mutex
}

func (s *switchTest) switchStmt() {
	switch s.state { // want `mu is not held while accessing state`
	case 1:
		s.state++ // want `mu is not held while accessing state`
	case 2:
		s.state++ // want `mu is not held while accessing state`
	}
}

func (s *switchTest) switchWithLock() {
	s.mu.Lock()
	switch s.state { // OK
	case 1:
		s.state++ // OK
	case 2:
		s.state++ // OK
	default:
		s.state++ // OK
	}
	s.mu.Unlock()
}

func (s *switchTest) typeSwitch(v interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch v.(type) {
	case int:
		s.state++ // OK
	case string:
		s.state++ // OK
	}
}

// ============================================================================
// Loops
// ============================================================================

type loopTest struct {
	counter int `protected_by:"mu"`
	mu      sync.Mutex
}

func (l *loopTest) forLoop() {
	for i := 0; i < 10; i++ {
		l.counter++ // want `mu is not held while accessing counter`
	}
}

func (l *loopTest) forLoopWithLock() {
	for i := 0; i < 10; i++ {
		l.mu.Lock()
		l.counter++ // OK
		l.mu.Unlock()
	}
}

func (l *loopTest) lockOutsideLoop() {
	l.mu.Lock()
	for i := 0; i < 10; i++ {
		l.counter++ // OK
	}
	l.mu.Unlock()
}

func (l *loopTest) rangeLoop() {
	items := []int{1, 2, 3}
	for _, item := range items {
		l.mu.Lock()
		l.counter += item // OK
		l.mu.Unlock()
	}
}

func (l *loopTest) infiniteLoop() {
	l.mu.Lock()
	for {
		l.counter++          // OK
		if l.counter > 100 { // OK
			break
		}
	}
	l.mu.Unlock()
}

// ============================================================================
// Multiple embedded fields
// ============================================================================

type embedA struct {
	a   int `protected_by:"muA"`
	muA sync.Mutex
}

type embedB struct {
	b   int `protected_by:"muB"`
	muB sync.Mutex
}

type multiEmbed struct {
	embedA
	embedB
	c int `protected_by:"muA"`
	d int `protected_by:"muB"`
}

func (m *multiEmbed) multipleEmbedded() {
	m.a++ // want `muA is not held while accessing a`
	m.b++ // want `muB is not held while accessing b`
	m.c++ // want `muA is not held while accessing c`
	m.d++ // want `muB is not held while accessing d`

	m.muA.Lock()
	m.a++ // OK
	m.c++ // OK
	m.b++ // want `muB is not held while accessing b`
	m.muA.Unlock()

	m.muB.Lock()
	m.b++ // OK
	m.d++ // OK
	m.a++ // want `muA is not held while accessing a`
	m.muB.Unlock()
}

func (m *multiEmbed) promotedMethods() {
	m.muA.Lock()
	m.embedA.a++ // OK (explicit path)
	m.muA.Unlock()

	m.muB.Lock()
	m.embedB.b++ // OK (explicit path)
	m.muB.Unlock()
}

// ============================================================================
// Shadowing scenarios
// ============================================================================

type ShadowInner struct {
	mu sync.Mutex
	x  int `protected_by:"mu"`
}

type ShadowOuter struct {
	ShadowInner
	mu sync.Mutex
	y  int `protected_by:"mu"`
}

func (s *ShadowOuter) shadowedLocks() {
	s.mu.Lock() // Locks ShadowOuter.mu
	s.y++       // OK
	s.x++       // want `mu is not held while accessing x`
	s.mu.Unlock()

	s.ShadowInner.mu.Lock() // Locks ShadowInner.mu explicitly
	s.x++                   // OK
	s.y++                   // want `mu is not held while accessing y`
	s.ShadowInner.mu.Unlock()

	// Lock both
	s.mu.Lock()
	s.ShadowInner.mu.Lock()
	s.x++ // OK
	s.y++ // OK
	s.ShadowInner.mu.Unlock()
	s.mu.Unlock()
}

// ============================================================================
// Defer with early returns
// ============================================================================

type deferTest struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

func (d *deferTest) deferWithEarlyReturn(cond bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.x++ // OK

	if cond {
		d.x++ // OK (defer ensures unlock)
		return
	}

	d.x++ // OK
}

func (d *deferTest) multipleDeferUnlocks() {
	d.mu.Lock()
	defer d.mu.Unlock()
	defer d.mu.Unlock() // Double unlock - runtime error but not our concern

	d.x++ // OK
}

// ============================================================================
// Select statements
// ============================================================================

type SelectTest struct {
	ch chan int
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

func (s *SelectTest) selectStmt() {
	select {
	case val := <-s.ch:
		s.x = val // want `mu is not held while accessing x`
	case s.ch <- s.x: // want `mu is not held while accessing x`
	default:
		s.x++ // want `mu is not held while accessing x`
	}
}

func (s *SelectTest) selectWithLock() {
	s.mu.Lock()
	select {
	case val := <-s.ch:
		s.x = val // OK
	case s.ch <- s.x: // OK
	default:
		s.x++ // OK
	}
	s.mu.Unlock()
}

// ============================================================================
// Complex protection paths with methods
// ============================================================================

type chainA struct {
	mu sync.Mutex
}

func (c *chainA) getMutex() *sync.Mutex {
	return &c.mu
}

type chainB struct {
	a chainA
}

func (c *chainB) getA() *chainA {
	return &c.a
}

type chainC struct {
	b chainB
	x int `protected_by:"b.a.mu"`
	y int `protected_by:"b.getA().getMutex()"`
}

func (c *chainC) chainedProtection() {
	c.x++ // want `mu is not held while accessing x`
	c.y++ // want `getMutex is not held while accessing y`

	c.b.a.mu.Lock()
	c.x++ // OK
	c.b.a.mu.Unlock()

	c.b.getA().getMutex().Lock()
	c.y++ // OK
	c.b.getA().getMutex().Unlock()
}

// ============================================================================
// Pointer receivers and values
// ============================================================================

type valueTest struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

func (v valueTest) valueReceiver() {
	v.x++ // Value receiver - copy, so no protection needed?
	// This is ambiguous - should probably still warn
}

func valueReceiverCaller() {
	var v valueTest
	v.valueReceiver() // Passes copy
}

// ============================================================================
// Unprotected access patterns
// ============================================================================

type unprotectedPatterns struct {
	init int `protected_by:"mu"` // But might be init-once pattern
	mu   sync.Mutex
}

func (u *unprotectedPatterns) initOnce() {
	// Common pattern: write once before any reads
	u.init = 42 // want `mu is not held while accessing init`
	// (Tool doesn't know about init-once)
}

// ============================================================================
// Named return values
// ============================================================================

type namedReturn struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

func (n *namedReturn) namedReturnValue() (result int) {
	n.mu.Lock()
	defer n.mu.Unlock()

	result = n.x // OK
	return
}

func (n *namedReturn) namedReturnNoLock() (result int) {
	result = n.x // want `mu is not held while accessing x`
	return
}

// ============================================================================
// Assignment operators
// ============================================================================

type assignOps struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

func (a *assignOps) compoundAssignments() {
	a.x += 1 // want `mu is not held while accessing x`
	a.x -= 1 // want `mu is not held while accessing x`
	a.x *= 2 // want `mu is not held while accessing x`
	a.x /= 2 // want `mu is not held while accessing x`
	a.x++    // want `mu is not held while accessing x`
	a.x--    // want `mu is not held while accessing x`

	a.mu.Lock()
	a.x += 1 // OK
	a.x -= 1 // OK
	a.x *= 2 // OK
	a.x /= 2 // OK
	a.x++    // OK
	a.x--    // OK
	a.mu.Unlock()
}

// ============================================================================
// Taking address
// ============================================================================

type addressOf struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

func (a *addressOf) takeAddress() {
	ptr := &a.x // want `mu is not held while accessing x`
	*ptr = 42   // Indirect access

	a.mu.Lock()
	ptr = &a.x // OK
	*ptr = 42  // OK (but we can't track through pointer)
	a.mu.Unlock()
}
