# lockguard

A Go static analysis tool (`golang.org/x/tools/go/analysis`) that detects lock-related bugs:
missing lock protections, deadlocks, invalid unlocks, and lock leaks.

## Build & test

```bash
go build ./...
go test ./...
```

The main test is `TestAnalyzer`, which runs the analysis over all files in `testdata/src/a/` using the `analysistest` framework. Every expected diagnostic is declared with a `// want` comment on the relevant line; unexpected diagnostics also fail the test.

`TestPathologicalCfg` pins the known CFG path-explosion limitation over `testdata/src/pathological` (deliberately not part of package `a` — it would hang the suite). It is known-failing and gated behind `LOCKGUARD_RUN_PATHOLOGICAL=1`; drop the gate when the path-explosion fix lands.

The analyzer runs on all packages except `runtime`, `internal`, and `unsafe` (see `run` in `analyzer.go`).

### Debug flags

Two analyzer flags (pass via `-args` under `go test`, or directly to the CLI):

- `-verbose` — firehose: dumps every generated CFG and lock-state tree. For deep-dives into a single function's analysis; the output is huge and interleaves across parallel package workers, so don't trust "last printed" attribution.
- `-tracefuncs` — lightweight progress trace: one `BEGIN`/`END` line (with duration) per analyzed function. To attribute a hang, find `BEGIN`s without a matching `END`; to find slow spots, sort the `END` durations.

```bash
go test -run TestAnalyzer -args -verbose
go build -o /tmp/lockguard ./cmd/lockguard && /tmp/lockguard -tracefuncs sync
```

## Architecture

### Two-phase design

1. **DFS traversal** (`analyzeCfg` in `analyzer.go`): walks every CFG path from entry to exit, forking the lock scope at each branch. Records *events* per path — one event per Lock/Unlock/access/leak on that specific path.
2. **Gather pass** (`gatherDiagnostics` in `event.go`): aggregates events grouped by `(pos, pathCode)` across all DFS paths and emits diagnostics.

**Why DFS over BFS/topological sort:** BFS merges lock states at join blocks, losing per-path information needed to correctly replay deferred functions at each exit point and to distinguish "definitely held" from "possibly held".

### Lock scope tree (`scope.go`)

`lockScope` holds a tree of `node`s keyed by `types.Object`. Each node tracks:
- `certainLockCount` / `uncertainLockCount` (regular Lock vs TryLock)
- `certainRLockCount` / `uncertainRLockCount`
- `acquirePos token.Pos` — source position of the Lock() call that last set this node's counts

At each CFG branch, `fork()` produces an independent deep copy of the scope so the two paths diverge independently.

`apply(path, pos)` dispatches to `lock()`, `lockUncertain()`, or `unlock()` based on the last element of the canonical path. `lock()` and `lockUncertain()` set `nd.acquirePos = pos`.

### Canonical paths (`locate.go`, `util.go`)

Field and function accesses are canonicalized through `types.Object` chains, handling embedded fields, promoted methods, and method expressions. Two expressions that refer to the same lock resolve to the same canonical path, so the scope tree can match them.

### Deferred calls (`analyzer.go`)

`deferredCallsStack [][][]any` is a stack of *defer scopes → defer branches → calls*:

- A **defer scope** is one function frame. Every `analyzeCfg` (a function body or a function literal) pushes one via `enterDeferScope`, plus a base branch for function-level injected defers.
- A **defer branch** is one CFG-block segment along the current DFS path. Each visited block pushes one via `enterDeferBranch`, popped on backtrack — so per-block defers vanish when the DFS leaves the block.

Each deferred call is either a `canonicalPath` (an annotation-injected unlock, replayed as `apply(..., token.NoPos)`) or an `*ast.CallExpr` (a user `defer`). `currentDeferred()` flattens the current scope's branches into LIFO execution order.

`processExitDeferred(exitPos, owned)` → `replayDeferred` applies those calls at each function exit, then collects leaks. A deferred `func` literal is re-entered via `analyzeCfg`; at each of its exit paths the DFS replays the literal's own defers, then the remaining outer defers, and only then collects leaks — so inner branches and defers are traversed recursively.

### Function literals and inline-IIFE decompression (`analyzer.go`)

`onExit` passed to `analyzeCfg` is effectively *the continuation of a compressed node*:

- **Top-level function** → "replay defers, report leaks, stop" (`owned = ownedAll`).
- **Callback / goroutine literal** → fresh empty scope (doesn't inherit caller locks), `owned = ownedAll`.
- **Nested inline literal** (`x := f(func(){…}())`) → inherits caller lock state (`fork`), `owned = ownedBy(funcLit)`; analyzed in isolation, no state flow.
- **Statement-level inline IIFE** (`func(){…}()` as its own statement) → *decompressed*: `processFrom` in `analyzeCfg` treats the literal's CFG as a compressed node and wires each of its exit paths to the node following the literal (`decompressInlineIIFE`). Locks on enclosing-scope variables flow into the rest of the function; the seam prunes the literal's own variables (`detachOwned`). Each exit path continues the enclosing function independently — no state merging, full path-sensitivity.

**Leak ownership (`ownedBy`):** which held roots a given exit reports is decided by an `owned func(types.Object) bool` predicate. A root belongs to a literal iff it is *declared lexically inside it*, decided by scope identity — walk `obj.Parent()` up to `pass.TypesInfo.Scopes[funcLit.Type]`. So receivers/params/locals of enclosing functions flow onward and are reported at the enclosing exit, while the literal's own parameters and locals are leak-checked at the literal.

### Event recorder (`event.go`)

Events are keyed by `(token.Pos, pathCode)`. The pathCode is a compact integer encoding of the `types.Object` chain (see `canonicalPathCoder`) that disambiguates two different objects that may share a source position (e.g., embedded field promotions).

**Uncertainty rule for leaks:**
```
uncertain = any_event.uncertain  ||  len(leakEvents) < exitPaths[pos]
```
"Not all DFS exit paths reported this lock as held" → possibly held.

### Lock-leak detection

Leaks are detected only at function exit (not at inner block boundaries — scope-boundary detection was removed because it is unsound in the presence of `defer`).

Two diagnostic modes, chosen by the gather pass:

| Condition | Report position | Message |
|---|---|---|
| All exit paths hold the lock (certain) | `acquirePos` (the Lock() call) | `'mu' acquired but never unlocked` |
| Only some paths hold it (uncertain) | `body.Rbrace` (function `}`) | `'mu' may not be unlocked at function exit` |

**Known limitation:** Locks acquired inside a `for` loop body that are never released are not detected. The DFS skips the back-edge from the post-block to the condition-block, so the lock state from inside the loop body never reaches a recorded function exit block.

### Directive annotations

Struct tags: `` `protected_by:"mu"` ``, `` `read_protected_by:"mu"` ``

Comment directives (on functions): `//lockguard:protected_by mu`

Both forms support dotted paths (`"b.a.mu"`) and method-call chains (`"getA().getMutex()"`).

### Diagnostic categories (`diagnostic.go`)

| Category | Meaning |
|---|---|
| `missing-lock` | Protected field accessed without required lock |
| `possibly-missing-lock` | Lock not held on all paths reaching the access |
| `deadlock` | Lock acquired while already definitely held |
| `possible-deadlock` | Lock acquired while possibly already held |
| `invalid-unlock` | Unlock called on lock that is not held |
| `possible-invalid-unlock` | Unlock called on lock that may not be held |
| `lock-leak` | Lock still held at function exit |

## Test data layout

```
testdata/src/a/
  basic.go               — basic protected_by coverage
  conditional_locking.go — if/else/switch conditional lock acquisition
  lock_leak.go           — function-exit leak detection
  iife_state.go          — statement-level inline IIFE state flow + leak ownership
  scope_boundary.go      — block-scoped mutexes, deferred unlocks
  try_lock.go            — TryLock / TryRLock semantics
  more_cases.go          — loops, switches, embedded fields, shadowing
  illegal_locking.go     — deadlock and invalid-unlock diagnostics
  flow_sensitive.go      — flow-sensitive lock state tracking
  ...
```
