# Lockguard

A Go static analysis tool that enforces lock protection rules on struct fields, methods, global variables and functions.

Lockguard complains when accesses to protected data (as defined by explicit protection rules) occur without the required lock(s) held, and flags common locking mistakes such as deadlocks and misaligned unlock calls.

## Installation

```sh
go install github.com/your-org/lockguard/cmd/lockguard@latest
```

Or use it with `go vet`:

```sh
go vet -vettool=$(which lockguard) ./...
```

## Annotating your code

Protection rules are specified with struct field tags, doc comments on functions, and doc comments on `var` declarations.

### Struct fields

Add a struct tag to declare which lock protects a field:

```go
type Counter struct {
  value int `protected_by:"mu"`
  mu    sync.Mutex
}
```

The tag value represents a dot-separated path from the struct receiver to the lock. The struct receiver is the context for such a path. The path can reference nested fields, method calls, and embedded fields:

```go
type Inner struct {
  mu sync.Mutex
}

type Outer struct {
  inner Inner
  extra int `protected_by:"inner.mu"`  // Nested field.
}

type Locker struct {
  mu sync.Mutex
}

func (l *Locker) Lock() *sync.Mutex {
  return &l.mu
}

type WithMethod struct {
  locker Locker
  data int `protected_by:"locker.Lock()"`  // Lock through a method.
}
```

Lockguard is smart with embedded fields.

```go
type WithLockEmbedded struct {
  sync.Mutex
  x int `protected_by:"sync.Mutex"` // Name embedded field.
}

type Outer struct {
  embeddedLock WithLockEmbedded
  x int `protected_by:"embeddedLock"` // Name embedded field.
}

func f() {
  var o Outer
  
  o.embeddedLock.Lock()
  defer o.embeddedLock.Lock()
  
  o.x++
  o.embeddedLock.x++ // This also works, as Lockguard knows locking o.embeddedLock automatically locks o.embeddedLock.(sync.Mutex).
}
```

### Functions and methods

Use a doc comment directive to declare that a function requires a lock to be held by the caller:

```go
type Server struct {
  mu sync.Mutex
  data int `protected_by:"mu"`
}

//lockguard:protected_by s.mu
func (s *Server) handleRequest() {
    // mu is assumed to be held here; accessing s.data is allowed.
    s.data++
}
```

To specify that functions are to be protected by multiple locks, use multiple directives:

```go
//lockguard:protected_by s.mu1
//lockguard:protected_by s.mu2
func (s *Server) updateBoth() { ... }
```

### Global variables

Protection rules for global variables are also specified by comment directives.

```go
var mu sync.Mutex

//lockguard:protected_by mu
var counter int
```

## Protection directives

Not all lock protection patterns are the same. To account for subtle variations in protection semantics, and for different types of locks
(`sync.Mutex` / `sync.RWMutex`), Lockguard has three protection rules, each with different requirements based on whether the variable is read or written.

| Tag / Directive | Meaning                                                                                                                                                                         |
|---|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `protected_by:"mu"` | Requires `mu.Lock()` for any read or write access for `sync.Mutex`. For `sync.RWMutex`, requires an `mu.RLock()` or a `mu.Lock()` for reads, and only a `mu.Lock()` for writes. |
| `read_protected_by:"mu"` | Requires at least a `mu.RLock()` for any access, read or write. Any access must be protected by either a `mu.RLock()` or a `mu.Lock()`.                                        |
| `write_protected_by:"mu"` | Requires a `mu.Lock()` for either read or write access.                                                                                                                         |

## Diagnostics

Diagnostic messages use the full dot-separated path to both the accessed field and the required lock, matching how you would write the expression in code (e.g. `s.data`, `s.mu`). For global variables there is no receiver prefix.

### Missing lock

```
writing 's.data' requires holding 's.mu'
reading 's.data' requires holding 's.mu'
```

The field or function was accessed without the required lock held. `writing` is used for assignments and increments; `reading` for all other accesses.

### Possibly missing lock

```
writing 's.data' requires holding 's.mu' (not held on all paths)
```

The required lock is not held on every path reaching this access — for example, it was acquired only inside one branch of an `if` statement or after a `TryLock` that is not unlocked on all branches.

### Deadlock

```
acquiring 's.mu' that is already held [deadlock]
acquiring 's.mu' that may be held [deadlock]
```

A lock is acquired while it is already definitively held (`already`) or possibly held (`may be`) on the current path. For `sync.RWMutex`, acquiring a write lock while a read lock is held also triggers this.

### Possible deadlock

```
acquiring 's.mu' that is already held [possible deadlock]
acquiring 's.mu' that may be held [possible deadlock]
```

Same as deadlock, but emitted for `TryLock` / `TryRLock` acquisitions where the outcome is uncertain.

### Invalid unlock

```
releasing 's.mu' that is not held
releasing 's.mu' that may not be held
releasing read lock on 's.mu' that is not held
releasing read lock on 's.mu' that may not be held
```

`Unlock` or `RUnlock` is called when the lock is definitely not held (`is not held`) or only possibly held (`may not be held`).

### Invalid annotation

```
expression doesn't locate a lock field mu
```

A struct tag or `//lockguard:` comment directive could not be parsed or resolved to a known lock object.

### Lock leak

```
's.mu' held at function exit (lock leak)
read lock on 's.mu' held at function exit (lock leak)
```

A lock is still definitively held when the function returns, meaning it was acquired but never released. This is typically a missing `Unlock` / `RUnlock` call, or a missing `defer` for an early-return path.

```
's.mu' possibly held at function exit (possible lock leak)
read lock on 's.mu' possibly held at function exit (possible lock leak)
```

A lock is possibly held at function exit — it was acquired on some paths but not released on all of them. This commonly occurs when a lock is acquired inside a branch (`if`, `switch`, `TryLock`) and not unconditionally released before the function returns.

## TryLock support

Lockguard understands `TryLock` and `TryRLock`. The lock state is propagated correctly into each branch:

```go
if s.mu.TryLock() {
    s.data++        // OK: lock is held on the true branch
    s.mu.Unlock()
} else {
    s.data++        // writing 's.data' requires holding 's.mu'
}

s.data++ // writing 's.data' requires holding 's.mu'
```

Compound conditions are also handled:

```go
// && : on the true branch, both locks are definitely held.
//       on the false branch, either could have been acquired (possibly held).
if t.mu1.TryLock() && t.mu2.TryLock() { ... }

// || : on the true branch, it is unknown which lock succeeded (possibly held).
//       on the false branch, neither was acquired.
if t.mu1.TryLock() || t.mu2.TryLock() { ... }
```

## Examples

### Cache with RWLock

```go
type Cache struct {
    data map[string]string `protected_by:"mu"`
    mu   sync.RWMutex
}

func (c *Cache) Set(key, value string) {
  // c.data[key] = value  // writing 'c.data' requires holding 'c.mu'

  // Must acquire write lock.
  c.mu.Lock()
  defer c.mu.Unlock()
  c.data[key] = value  // OK
}

func (c *Cache) Get(key string) string {
  // return c.data[key]  // reading 'c.data' requires holding 'c.mu'

  // Can acquire read lock.
  c.mu.RLock()
  defer c.mu.RUnlock()
  return c.data[key]  // OK
}
```

### Protected functions as API contracts

```go
//lockguard:protected_by s.mu
func (s *Server) lockedHelper() {
    s.sharedState++  // OK: mu is assumed held by the caller
}

func (s *Server) PublicMethod() {
    s.lockedHelper()  // reading 's.lockedHelper' requires holding 's.mu'

    s.mu.Lock()
    defer s.mu.Unlock()
    s.lockedHelper()  // OK
}
```

### Cross-package locks

A lock defined in package `b` can be referenced using its import path:

```go
//lockguard:protected_by globalMut
//lockguard:protected_by b.GlobalMut
var sharedData int
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `-lockguard.verbose` | `false` | Print internal CFG and lock-state debug output |

## Known limitations

- **Struct literals**: field accesses inside composite literals are not yet checked.
- **Cross-package facts**: protection facts are exported for use by other packages, but importing facts from dependencies is not yet implemented.
- **Back edges / loops**: lock state is not propagated around loop back edges, so patterns like `for { mu.Lock() }` are not analyzed across iterations.
