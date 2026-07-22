package a

import "sync"

// This file pins the memoization gate for irreducible CFGs (memo.go). A goto-built multi-entry
// cycle is an irreducible SCC; the analyzer must NOT memoize blocks inside it, because two arrivals
// carrying the same lock state can still have different futures (different cycle-mates on the DFS
// stack). If such a block were wrongly memoized, the diagnostics below would be silently dropped.

type irreducibleS struct {
	x  int `protected_by:"mu"`
	mu sync.Mutex
}

// twoEntryCycle builds the classic two-entry cycle {X, Y}: entered at X from the locking branch and
// at Y from the bare branch (see the design note, Figure 1). The access at X is reached with the
// lock held (via the p-branch) and without it (via the cycle re-entry Y->X on the !p path), so it is
// possibly-missing; the unlock on the !p path releases a lock that was never taken. Memoizing block
// Y with the post-unlock (empty) state would prune the bare entry and drop both findings.
func (s *irreducibleS) twoEntryCycle(p, q bool) {
	if p {
		s.mu.Lock()
		goto X
	}
	goto Y
X:
	s.x++ // want `writing 's\.x' requires holding 's\.mu' \(not held on all paths\)`
	s.mu.Unlock() // want `releasing 'mu' that may not be held`
Y:
	if q {
		goto X
	}
}