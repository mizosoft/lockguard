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
		c.x++ // want `writing 'c\.x' requires holding 'c\.mu'`
	}

	c.x++ // want `writing 'c\.x' requires holding 'c\.mu'`
}

func (c *controlFlow) lockInBranch(cond bool) {
	if cond {
		c.mu.Lock()
	}
	c.x++ // want `writing 'c\.x' requires holding 'c\.mu' \(not held on all paths\)`
	if cond {
		// Currently lockguard can't know what cond implies from above.
		c.mu.Unlock() // want `releasing 'mu' that may not be held`
	}
} // want `'c\.mu' possibly held at function exit \(possible lock leak\)`

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
	switch s.state { // want `reading 's\.state' requires holding 's\.mu'`
	case 1:
		s.state++ // want `writing 's\.state' requires holding 's\.mu'`
	case 2:
		s.state++ // want `writing 's\.state' requires holding 's\.mu'`
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
		l.counter++ // want `writing 'l\.counter' requires holding 'l\.mu'`
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
	m.a++ // want `writing 'm\.embedA\.a' requires holding 'm\.embedA\.muA'`
	m.b++ // want `writing 'm\.embedB\.b' requires holding 'm\.embedB\.muB'`
	m.c++ // want `writing 'm\.c' requires holding 'm\.embedA\.muA'`
	m.d++ // want `writing 'm\.d' requires holding 'm\.embedB\.muB'`

	m.muA.Lock()
	m.a++ // OK
	m.c++ // OK
	m.b++ // want `writing 'm\.embedB\.b' requires holding 'm\.embedB\.muB'`
	m.muA.Unlock()

	m.muB.Lock()
	m.b++ // OK
	m.d++ // OK
	m.a++ // want `writing 'm\.embedA\.a' requires holding 'm\.embedA\.muA'`
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
	s.x++       // want `writing 's\.ShadowInner\.x' requires holding 's\.ShadowInner\.mu'`
	s.mu.Unlock()

	s.ShadowInner.mu.Lock() // Locks ShadowInner.mu explicitly
	s.x++                   // OK
	s.y++                   // want `writing 's\.y' requires holding 's\.mu'`
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
		s.x = val // want `writing 's\.x' requires holding 's\.mu'`
	case s.ch <- s.x: // want `reading 's\.x' requires holding 's\.mu'`
	default:
		s.x++ // want `writing 's\.x' requires holding 's\.mu'`
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
	c.x++ // want `writing 'c\.x' requires holding 'c\.b\.a\.mu'`
	c.y++ // want `writing 'c\.y' requires holding 'c\.b\.getA\.getMutex'`

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
	v.x++ // want `writing 'v\.x' requires holding 'v\.mu'`
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
	u.init = 42 // want `writing 'u\.init' requires holding 'u\.mu'`
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
	result = n.x // want `reading 'n\.x' requires holding 'n\.mu'`
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
	a.x += 1 // want `writing 'a\.x' requires holding 'a\.mu'`
	a.x -= 1 // want `writing 'a\.x' requires holding 'a\.mu'`
	a.x *= 2 // want `writing 'a\.x' requires holding 'a\.mu'`
	a.x /= 2 // want `writing 'a\.x' requires holding 'a\.mu'`
	a.x++    // want `writing 'a\.x' requires holding 'a\.mu'`
	a.x--    // want `writing 'a\.x' requires holding 'a\.mu'`

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
	ptr := &a.x // want `reading 'a\.x' requires holding 'a\.mu'`
	*ptr = 42   // Indirect access

	a.mu.Lock()
	ptr = &a.x // OK
	*ptr = 42  // OK (but we can't track through pointer)
	a.mu.Unlock()
}
