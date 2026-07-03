
# TODO

## Correctness gaps

### Loop back-edge skipping

The DFS skips back-edges to avoid infinite recursion, so any lock held across a loop iteration
is invisible to the tool. Locks acquired inside a loop body that are never released (or
conditionally released) produce no diagnostic. Possible approaches: bounded unrolling,
fixed-point iteration on the loop header state, or a separate loop-body scan pass.

### No inter-procedural analysis

Locks acquired in helper functions and returned to callers, or released in helpers that receive
the lock as an argument, are invisible. The `FactTypes` slot is wired up but facts are never
exported or imported across package boundaries.

## Missing language coverage

- **Struct literals:** field writes inside composite literals are not checked against protections.
- **Index expressions:** `s.arr[i]` where `arr` is a protected field is not checked.
- **Pointer aliasing:** `mu := &s.mu; mu.Lock()` does not match `s.mu` in the lock scope tree.
  More broadly, locks passed by pointer through assignment or function arguments are not tracked.
- **Assignment graph:** protecting a field via a value returned from a function or stored through
  an intermediate variable is not tracked.

## Pinned known-limitation test assertions

`testdata/src/a/flow_sensitive.go` asserts (via `// want`) several diagnostics that are in fact
**false positives**, and drops one **false negative**, in order to pin current behavior. Each is
marked with a `TODO(limitation)` comment in the file. Revisit these assertions when the underlying
gap is closed:

- **Pointer aliasing / inter-procedural** (`lockViaReturn`, `helperLock`, `helperUnlock`,
  `lockViaHelper`, `lockHere`, `unlockThere`): locks reached through a returned pointer or passed to
  a helper are wrongly flagged as missing-lock / leak / invalid-unlock. Drop these once aliasing and
  interprocedural lock state are tracked (see "No inter-procedural analysis" and "Pointer aliasing").
- **Index expressions** (`arrayOfProtected`, `sliceOfProtected`): `arr[0].x++` on an array/slice of
  protected structs is not flagged though it should be. Add the diagnostic once index-expression
  bases are resolved (see "Index expressions").

## Annotation / directive gaps

- **Fact export:** protections declared in one package are not visible to packages that embed or
  use those types. Add fact export/import so cross-package struct embedding works correctly.
- **`read_protected_by` / `write_protected_by`:** distinguish accesses that require only RLock
  (reads) from those that require Lock (writes), independent of the field-level directive.
- **Init-once pattern:** consider an annotation to suppress warnings for fields written exactly
  once before a struct is shared (e.g. constructor functions).

## Usability

- **Remove the dev gate:** the `pass.Pkg.Name() != "a"` guard in `analyzer.go` must be removed
  before shipping; replace it with the commented-out runtime/internal skip block.
- **Value receivers:** value receiver copies silently drop lock state; warn or document the
  limitation.
- **Goroutine-shared fields:** when a protected field is accessed from multiple goroutines via
  `go` statements without a lock, flag it even if individual accesses appear locally protected.
