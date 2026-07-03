# Lockguard

A Go static analysis tool that enforces lock protection rules on struct fields, methods, global variables, and functions. Lockguard complains when protected data is accessed without the required lock held, and catches common mistakes like deadlocks, invalid unlocks, and lock leaks.

## Installation

```sh
go install github.com/your-org/lockguard/cmd/lockguard@latest
```

Or use it with `go vet`:

```sh
go vet -vettool=$(which lockguard) ./...
```

## Annotating your code

Protection rules are declared with struct field tags and `//lockguard:` comment directives.

### Struct fields

Add a struct tag to declare which lock protects a field:

```go
type Counter struct {
    value int `protected_by:"mu"`
    mu    sync.Mutex
}
```

The tag value is a dot-separated path from the struct receiver to the lock. It can reference nested fields, method calls, and embedded fields:

```go
type Inner struct {
    mu sync.Mutex
}

type Outer struct {
    inner Inner
    extra int `protected_by:"inner.mu"` // Nested field.
}

type Locker struct {
    mu sync.Mutex
}

func (l *Locker) Lock() *sync.Mutex {
    return &l.mu
}

type WithMethod struct {
    locker Locker
    data   int `protected_by:"locker.Lock()"` // Lock through a method.
}
```

Lockguard is smart about embedded fields:

```go
type WithLockEmbedded struct {
    sync.Mutex
    x int `protected_by:"sync.Mutex"`
}

type Outer struct {
    embeddedLock WithLockEmbedded
    x            int `protected_by:"embeddedLock"`
}

func f() {
    var o Outer
    o.embeddedLock.Lock()
    defer o.embeddedLock.Unlock()

    o.x++                  // OK
    o.embeddedLock.x++     // Also OK — locking embeddedLock covers its fields too.
}
```

### Functions and methods

Use a doc comment directive to declare that a function requires a lock to be held by the caller:

```go
type Server struct {
    mu   sync.Mutex
    data int `protected_by:"mu"`
}

//lockguard:protected_by s.mu
func (s *Server) handleRequest() {
    s.data++ // OK — mu is assumed held by the caller.
}
```

To require multiple locks:

```go
//lockguard:protected_by s.mu1
//lockguard:protected_by s.mu2
func (s *Server) updateBoth() { ... }
```

### Global variables

Protection rules for global variables are also specified by comment directives:

```go
var mu sync.Mutex

//lockguard:protected_by mu
var counter int
```

### Protection directives

Not all lock protection patterns are the same. Lockguard has three directives that differ in their read vs. write requirements:

| Tag / Directive | Meaning |
|---|---|
| `protected_by:"mu"` | Requires `mu.Lock()` for any read or write. For `sync.RWMutex`, `mu.RLock()` is sufficient for reads. |
| `read_protected_by:"mu"` | Requires at least `mu.RLock()` for any access. |
| `write_protected_by:"mu"` | Requires `mu.Lock()` for both reads and writes. |

## What Lockguard checks

### Missing lock

The most basic case: accessing a protected field without holding the required lock.

```go
func (s *Server) unsafeWrite() {
    s.data++ // writing 's.data' requires holding 's.mu'
}

func (s *Server) safeWrite() {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.data++ // OK
}
```

`writing` is used for assignments, increments and decrements; `reading` for all other accesses.

### Possibly missing lock

When the lock is not held on every path reaching an access — for example, acquired only inside one branch of an `if` statement — Lockguard emits a warning with a "less sure" tone.

```go
func (s *Server) conditionalWrite(cond bool) {
    if cond {
        s.mu.Lock()
    }
    s.data++ // writing 's.data' requires holding 's.mu' (not held on all paths)
}
```

### Lock leak

A lock that is still held when a function returns was acquired but never released makes a lock leak. This is commonly a missing `defer` on an early-return path.

```go
func (s *Server) leaksLock() {
    s.mu.Lock() // 's.mu' acquired but never unlocked
    s.data++
}

func (s *Server) noLeak() {
    s.mu.Lock()
    defer s.mu.Unlock() // OK — defer fires on all return paths
    s.data++
}
```

A certain leak, every path out of the function holds the lock — is reported at the `Lock()` call that acquired it.

When the lock is held on only *some* of the paths reaching the exit — acquired or released on a branch — the leak is uncertain and is reported at the closing brace as *may not be unlocked*:

```go
func (s *Server) leaksOnEarlyReturn(cond bool) {
    s.mu.Lock()
    if cond {
        return // leaves 's.mu' held; the other path unlocks it — a defer would fix this
    }
    s.data++
    s.mu.Unlock()
} // 's.mu' may not be unlocked at function exit

func (s *Server) possiblyLeaks(cond bool) {
    if cond {
        s.mu.Lock()
    }
    s.data++ // writing 's.data' requires holding 's.mu' (not held on all paths)
} // 's.mu' may not be unlocked at function exit
```

### Deadlock

Acquiring a lock that is already held will block forever on a non-reentrant mutex.

```go
func (s *Server) deadlock() {
    s.mu.Lock()
    s.mu.Lock() // acquiring 's.mu' that is already held [deadlock]
    defer s.mu.Unlock()
}
```

When the lock is only possibly held on the current path, the diagnostic reports a possible deadlock instead:

```go
func (s *Server) possibleDeadlock(cond bool) {
    if cond {
        s.mu.Lock()
    }
    s.mu.Lock() // acquiring 's.mu' may cause deadlock: may already be held
    defer s.mu.Unlock()
}
```

### Invalid unlock

Calling `Unlock` when the lock is not held is also flagged.

```go
func (s *Server) doubleUnlock() {
    s.mu.Lock()
    s.mu.Unlock()
    s.mu.Unlock() // releasing 's.mu' that is not held
}

func (s *Server) conditionalUnlock(cond bool) {
    if cond {
        s.mu.Lock()
    }
    s.mu.Unlock() // releasing 's.mu' that may not be held
}
```

### Invalid annotation

If a struct tag or `//lockguard:` directive cannot be resolved to a known lock object, Lockguard will tell you:

```
expression doesn't locate a lock field mu
```

## TryLock support

Lockguard understands `TryLock` and `TryRLock` and propagates the lock state correctly into each branch:

```go
func (s *Server) tryWrite() {
    if s.mu.TryLock() {
        s.data++ // OK — lock is definitely held on the true branch
        s.mu.Unlock()
    } else {
        s.data++ // writing 's.data' requires holding 's.mu'
    }

    s.data++ // writing 's.data' requires holding 's.mu'
}
```

The early-return guard pattern is also understood — after the `if` block, the lock is known to be held:

```go
func (s *Server) guardedWrite() {
    if !s.mu.TryLock() {
        return
    }
    defer s.mu.Unlock()
    s.data++ // OK
}
```

Compound `TryLock` conditions are handled too:

```go
// &&: on the true branch, both locks are definitely held.
//     On the false branch, either could have been acquired before the short-circuit (possibly held).
if t.mu1.TryLock() && t.mu2.TryLock() { ... }

// ||: on the true branch, it is unknown which lock succeeded (possibly held).
//     On the false branch, neither was acquired.
if t.mu1.TryLock() || t.mu2.TryLock() { ... }
```

## Inline function literals

An immediately-invoked function literal written as its own statement - `func() { ... }()` - is analyzed as if its body were spliced into the enclosing function. Locks it takes or releases on variables from the enclosing scope carry over to the code that follows it:

```go
func (s *Server) inlineLocks() {
    func() {
        s.mu.Lock() // 's.mu' acquired but never unlocked
    }()
    s.data++ // OK — s.mu is held; the lock flowed out of the literal
}
```

If the literal releases what it took (for example with a `defer` inside it), the lock is correctly seen as released afterwards:

```go
func (s *Server) inlineLockUnlock() {
    func() {
        s.mu.Lock()
        defer s.mu.Unlock()
        s.data++ // OK
    }()
    s.data++ // writing 's.data' requires holding 's.mu' — released inside the literal
}
```

Locks taken on variables **declared inside the literal** belong to the literal: they are leak-checked at the literal itself and do not affect the enclosing function:

```go
func useScratchLock() {
    func() {
        var mu sync.Mutex
        mu.Lock() // 'mu' acquired but never unlocked
    }()
}
```

Each exit path of the literal is followed independently, so a lock taken on only some of its paths is reported as possibly held, exactly as for a plain `if`:

```go
func (s *Server) inlineConditional(cond bool) {
    func() {
        if cond {
            s.mu.Lock()
        }
    }()
    s.data++ // writing 's.data' requires holding 's.mu' (not held on all paths)
} // 's.mu' may not be unlocked at function exit
```

This state continuation applies to statement-level literals only; see [Known limitations](#known-limitations) for literals nested inside larger expressions.

## Flags

| Flag | Default | Description |
|---|---|---|
| `-lockguard.verbose` | `false` | Print internal CFG and lock-state debug output |

## Known limitations

- **Struct literals**: field accesses inside composite literals are not yet checked.
- **Cross-package facts**: protection facts are exported for use by other packages, but importing facts from dependencies is not yet implemented.
- **Back edges / loops**: lock state is not propagated around loop back edges, so patterns like `for { mu.Lock() }` are not analyzed across iterations.
- **In-expression function literals**: only statement-level immediately-invoked literals (`func() { ... }()` on their own line) have their lock effects flow into the enclosing function. A literal nested inside a larger expression — for example `x := compute(func() int { ... }())` or `y := a + func() int { ... }()` — is analyzed in isolation, so locks it takes on enclosing-scope variables do not carry over to the surrounding code. Leaks on the literal's own variables are still detected.
