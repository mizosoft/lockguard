# Lockguard

A Go static analysis tool that enforces lock protection rules on struct fields, methods, and global variables and functions.

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
  data int `protected_by:"locker.Lock()"`  // Lock through a method.
}
```

### Functions and methods

Use a doc comment directive to declare that a function requires a lock to be held by the caller:

```go
//lockguard:protected_by s.mu
func (s *Server) handleRequest() {
    // mu is assumed held here; accessing s.data is allowed.
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

### Missing lock

```
mut is not held while accessing i
```

The field or function was accessed without the required lock held.

### Possibly missing lock

```
mut is possibly not held while accessing i
```

Emitted when lockguard cannot determine statically whether the lock is held on all paths reaching this access (e.g. after a `TryLock` with no corresponding `Unlock` on all branches).

### Deadlock

```
deadlock: mu - already locked
```

A lock is acquired while it is already definitively held. For `sync.RWMutex`, acquiring a write lock while a read lock is already held also triggers this.

### Misaligned unlock

```
mu - unlocking a non-locked lock
mu - read-unlocking a non-locked lock
```

An `Unlock` or `RUnlock` is called when the lock is not currently held.

## TryLock support

Lockguard understands `TryLock` and `TryRLock`. The lock state is propagated correctly into each branch:

```go
if s.mu.TryLock() {
    s.data++        // OK: lock is held on the true branch
    s.mu.Unlock()
} else {
    s.data++        // diagnostic: mu is not held while accessing data
}

s.data++ // diagnostic: mu is not held while accessing data
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
  // c.data[key] = value  // would be flagged: mu is not held
  
  // Must acquire write lock.
  c.mu.Lock()
  defer c.mu.Unlock()
  c.data[key] = value  // OK
}

func (c *Cache) Get(key string) string {
  // return c.data[key]  // would be flagged: mu is not held

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
    s.lockedHelper()  // flagged: mu is not held while accessing lockedHelper

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
| `-lockguard.debug` | `false` | Print internal CFG and lock-state debug output |

## Known limitations

- **Struct literals**: field accesses inside composite literals are not yet checked.
- **Cross-package facts**: protection facts are exported for use by other packages, but importing facts from dependencies is not yet implemented.
- **Back edges / loops**: lock state is not propagated around loop back edges, so patterns like `for { mu.Lock() }` are not analyzed across iterations.
- **Early-return TryLock guard**: the pattern `if !mu.TryLock() { return }` does not yet inject lock state into the continuation block.
