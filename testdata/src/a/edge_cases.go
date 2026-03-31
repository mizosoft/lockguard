package a

import (
	"sync"
	"sync/atomic"
)

// ============================================================================
// Atomic operations on protected fields
// ============================================================================

type atomicTest struct {
	counter int64 `protected_by:"mu"` // Protected but using atomics
	mu      sync.Mutex
}

func (a *atomicTest) atomicAccess() {
	// Tool should still warn even though atomic operations are safe
	atomic.AddInt64(&a.counter, 1) // want `reading 'a\.counter' requires holding 'a\.mu'`

	a.mu.Lock()
	atomic.AddInt64(&a.counter, 1) // OK
	a.mu.Unlock()
}

// ============================================================================
// Struct comparison with protected fields
// ============================================================================

type comparable struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

func (c *comparable) compare(other *comparable) bool {
	return c.x == other.x // want `reading 'c\.x' requires holding 'c\.mu'` `reading 'other\.x' requires holding 'other\.mu'`
}

func (c *comparable) compareHalfSafe(other *comparable) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.x == other.x // want `reading 'other\.x' requires holding 'other\.mu'`
}

func (c *comparable) safeCompare(other *comparable) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	other.mu.Lock()
	defer other.mu.Unlock()
	return c.x == other.x
}

// ============================================================================
// Pointers to protected fields
// ============================================================================

type pointerFields struct {
	ptr *int `protected_by:"mu"`
	mu  sync.Mutex
}

func (p *pointerFields) dereferenceProtected() {
	*p.ptr = 42 // want `writing 'p\.ptr' requires holding 'p\.mu'`

	p.mu.Lock()
	*p.ptr = 42 // OK
	p.mu.Unlock()
}

func (p *pointerFields) reassignProtected() {
	newVal := 100
	p.ptr = &newVal // want `writing 'p\.ptr' requires holding 'p\.mu'`

	p.mu.Lock()
	p.ptr = &newVal // OK
	p.mu.Unlock()
}

// ============================================================================
// Type assertions on protected fields
// ============================================================================

type typeAssertions struct {
	iface interface{} `protected_by:"mu"`
	mu    sync.Mutex
}

func (t *typeAssertions) typeAssert() {
	_ = t.iface.(int) // want `reading 't\.iface' requires holding 't\.mu'`

	t.mu.Lock()
	_ = t.iface.(int) // OK
	t.mu.Unlock()
}

func (t *typeAssertions) typeSwitch() {
	switch t.iface.(type) { // want `reading 't\.iface' requires holding 't\.mu'`
	case int:
	case string:
	}

	t.mu.Lock()
	switch t.iface.(type) { // OK
	case int:
	case string:
	}
	t.mu.Unlock()
}

// ============================================================================
// Variadic functions with protected arguments
// ============================================================================

type variadicTest struct {
	values []int `protected_by:"mu"`
	mu     sync.Mutex
}

func sum(vals ...int) int {
	total := 0
	for _, v := range vals {
		total += v
	}
	return total
}

func (v *variadicTest) passToVariadic() {
	_ = sum(v.values...) // want `reading 'v\.values' requires holding 'v\.mu'`

	v.mu.Lock()
	_ = sum(v.values...) // OK
	v.mu.Unlock()
}

// ============================================================================
// Closures capturing protected fields
// ============================================================================

type closureTest struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

func (c *closureTest) closureCapture() {
	fn := func() int {
		return c.x // want `reading 'c\.x' requires holding 'c\.mu'`
	}
	_ = fn()

	c.mu.Lock()
	fn2 := func() int {
		return c.x // want `reading 'c\.x' requires holding 'c\.mu'`
	}
	c.mu.Unlock()

	c.mu.Lock()
	func() int {
		return c.x // OK, called inline
	}()
	c.mu.Unlock()

	_ = fn2() // Called without lock (but tool may not catch this)
}

func (c *closureTest) closureWithLockInside() {
	fn := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.x++ // OK
	}
	fn()
}

// ============================================================================
// Struct embedding with name collision
// ============================================================================

type Base1 struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

type Base2 struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

type MultiBase struct {
	Base1
	Base2
	y int `protected_by:"Base1.mu"`
	z int `protected_by:"Base2.mu"`
}

func (m *MultiBase) ambiguousAccess() {
	m.y++ // want `writing 'm\.y' requires holding 'm\.Base1\.mu'`
	m.z++ // want `writing 'm\.z' requires holding 'm\.Base2\.mu'`

	// Can't access m.x or m.mu due to ambiguity
	// m.x++ // This would be a compile error

	m.Base1.mu.Lock()
	m.Base1.x++ // OK
	m.y++       // OK
	m.z++       // want `writing 'm\.z' requires holding 'm\.Base2\.mu'`
	m.Base1.mu.Unlock()
}

// ============================================================================
// Const fields (shouldn't need protection but tagged anyway)
// ============================================================================

type constFields struct {
	immutable int `protected_by:"mu"` // Const-like but Go doesn't have const fields
	mu        sync.Mutex
}

func (c *constFields) readImmutable() {
	_ = c.immutable // want `reading 'c\.immutable' requires holding 'c\.mu'`
}

// ============================================================================
// Blank identifier in assignments
// ============================================================================

type blankTest struct {
	x, y int `protected_by:"mu"`
	mu   sync.Mutex
}

func (b *blankTest) blankAssignment() {
	_, _ = b.x, b.y // want `reading 'b\.x' requires holding 'b\.mu'` `reading 'b\.y' requires holding 'b\.mu'`

	b.mu.Lock()
	_, _ = b.x, b.y // OK
	b.mu.Unlock()
}

// ============================================================================
// Method values (bound methods)
// ============================================================================

type methodValue struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

//lockguard:protected_by m.mu
func (m *methodValue) protectedMethod() {
	m.x++ // OK (method requires lock)
}

func (m *methodValue) methodAsValue() {
	fn := m.protectedMethod // want `reading 'm\.protectedMethod' requires holding 'm\.mu'`

	m.mu.Lock()
	fn = m.protectedMethod
	fn()
	m.mu.Unlock()
}

// ============================================================================
// Recursive functions with locks
// ============================================================================

type recursiveTest struct {
	depth int `protected_by:"mu"`
	mu    sync.Mutex
}

func (r *recursiveTest) recursiveWithLock(n int) {
	if n <= 0 {
		return
	}

	r.mu.Lock()
	r.depth++ // OK
	r.mu.Unlock()

	r.recursiveWithLock(n - 1)
}

func (r *recursiveTest) recursiveNoLock(n int) {
	if n <= 0 {
		return
	}

	r.depth++ // want `writing 'r\.depth' requires holding 'r\.mu'`
	r.recursiveNoLock(n - 1)
}

// ============================================================================
// Labels and labeled statements
// ============================================================================

type labelTest struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

func (l *labelTest) labeledLoop() {
outer:
	for i := 0; i < 10; i++ {
		for j := 0; j < 10; j++ {
			if j == 5 {
				l.x++ // want `writing 'l\.x' requires holding 'l\.mu'`
				break outer
			}
		}
	}
}

func (l *labelTest) labeledLoopWithLock() {
	l.mu.Lock()
	defer l.mu.Unlock()

outer:
	for i := 0; i < 10; i++ {
		for j := 0; j < 10; j++ {
			if j == 5 {
				l.x++ // OK
				break outer
			}
		}
	}
}

// ============================================================================
// Complex nested defer with multiple locks
// ============================================================================

type multiDeferTest struct {
	a   int `protected_by:"mu1"`
	b   int `protected_by:"mu2"`
	mu1 sync.Mutex
	mu2 sync.Mutex
}

func (m *multiDeferTest) nestedDefer() {
	m.mu1.Lock()
	defer m.mu1.Unlock()

	m.a++ // OK

	m.mu2.Lock()
	defer m.mu2.Unlock()

	m.a++ // OK
	m.b++ // OK
}

func (m *multiDeferTest) deferOrder() {
	defer m.mu1.Unlock()
	defer m.mu2.Unlock()

	m.mu2.Lock()
	m.mu1.Lock()

	m.a++ // OK
	m.b++ // OK
}

// ============================================================================
// Type conversion with protected fields
// ============================================================================

type aliasedType int

type typeConversion struct {
	x  int         `protected_by:"mu"`
	y  aliasedType `protected_by:"mu"`
	mu sync.Mutex
}

func (t *typeConversion) convert() {
	_ = int(t.y)         // want `reading 't\.y' requires holding 't\.mu'`
	_ = aliasedType(t.x) // want `reading 't\.x' requires holding 't\.mu'`

	t.mu.Lock()
	_ = int(t.y)         // OK
	_ = aliasedType(t.x) // OK
	t.mu.Unlock()
}

// ============================================================================
// Unary expressions on protected fields
// ============================================================================

type unaryTest struct {
	x  int  `protected_by:"mu"`
	b  bool `protected_by:"mu"`
	mu sync.Mutex
}

func (u *unaryTest) unaryOps() {
	_ = -u.x // want `reading 'u\.x' requires holding 'u\.mu'`
	_ = +u.x // want `reading 'u\.x' requires holding 'u\.mu'`
	_ = !u.b // want `reading 'u\.b' requires holding 'u\.mu'`

	u.mu.Lock()
	_ = -u.x // OK
	_ = +u.x // OK
	_ = !u.b // OK
	u.mu.Unlock()
}

// ============================================================================
// Function call on protected struct method
// ============================================================================

type methodStruct struct {
	mu sync.Mutex
}

func (m *methodStruct) doSomething() int {
	return 42
}

type hasMethodStruct struct {
	ms methodStruct `protected_by:"mu"`
	mu sync.Mutex
}

func (h *hasMethodStruct) callProtectedMethod() {
	_ = h.ms.doSomething() // want `reading 'h\.ms' requires holding 'h\.mu'`

	h.mu.Lock()
	_ = h.ms.doSomething() // OK
	h.mu.Unlock()
}

// ============================================================================
// Init functions (package initialization)
// ============================================================================

var globalProtected struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

func init() {
	// During init, no concurrency exists yet
	globalProtected.x = 42 // want `writing 'globalProtected\.x' requires holding 'globalProtected\.mu'`
}

// ============================================================================
// String concatenation with protected fields
// ============================================================================

type stringTest struct {
	name string `protected_by:"mu"`
	mu   sync.Mutex
}

func (s *stringTest) concat() {
	_ = "Hello " + s.name // want `reading 's\.name' requires holding 's\.mu'`

	s.mu.Lock()
	_ = "Hello " + s.name // OK
	s.mu.Unlock()
}

// ============================================================================
// Nested struct literals with protected fields
// ============================================================================

type literalInner struct {
	x int
}

type literalOuter struct {
	inner literalInner `protected_by:"mu"`
	mu    sync.Mutex
}

func (l *literalOuter) structLiteral() {
	l.inner = literalInner{x: 42} // want `writing 'l\.inner' requires holding 'l\.mu'`

	l.mu.Lock()
	l.inner = literalInner{x: 42} // OK
	l.mu.Unlock()
}

// ============================================================================
// Len, cap, make on protected slices
// ============================================================================

type builtinTest struct {
	slice []int       `protected_by:"mu"`
	m     map[int]int `protected_by:"mu"`
	ch    chan int    `protected_by:"mu"`
	mu    sync.Mutex
}

func (b *builtinTest) builtinFunctions() {
	_ = len(b.slice) // want `reading 'b\.slice' requires holding 'b\.mu'`
	_ = cap(b.slice) // want `reading 'b\.slice' requires holding 'b\.mu'`
	_ = len(b.m)     // want `reading 'b\.m' requires holding 'b\.mu'`
	_ = len(b.ch)    // want `reading 'b\.ch' requires holding 'b\.mu'`
	_ = cap(b.ch)    // want `reading 'b\.ch' requires holding 'b\.mu'`
	close(b.ch)      // want `reading 'b\.ch' requires holding 'b\.mu'`

	b.mu.Lock()
	_ = len(b.slice) // OK
	_ = cap(b.slice) // OK
	_ = len(b.m)     // OK
	b.mu.Unlock()
}
