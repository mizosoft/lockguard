package lockguard

import (
	"go/ast"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/cfg"
)

// This file implements the structural side of visit memoization (see analyzeCfg).
//
// The DFS explores only simple paths, skipping every retreating edge (an edge whose target is on
// the DFS stack). Memoizing a visit on (block, state) is exact iff the traversal from that pair
// is independent of the rest of the stack. That holds everywhere except inside *irreducible*
// SCCs — SCCs containing a retreating edge that is not a back edge (its target does not dominate
// its source), i.e. multi-entry cycles, constructible in Go only with goto. There, two arrivals
// with equal (block, state) can differ in which cycle-mates are blocked (on the stack) and hence
// have genuinely different futures; pruning the second would silently drop diagnostics.
//
// Resolution: precompute, per CFG, which blocks belong to irreducible SCCs, and never memoize
// those. Blocks of reducible SCCs (all structured control flow, and most gotos) get exact
// (block, state) memoization. See the design note for the proofs.

// cfgInfo holds the per-CFG structural facts needed by the memoization gate.
type cfgInfo struct {
	// irreducible[b.Index] reports whether b belongs to an irreducible SCC.
	irreducible []bool
}

// computeCfgInfo computes dominators, SCCs, and the irreducibility marking for g: an SCC is
// irreducible iff it contains a retreating edge (under one structural DFS from the entry) whose
// target does not dominate its source. Reducibility is DFS-order independent, so one traversal
// suffices.
func computeCfgInfo(g *cfg.CFG) *cfgInfo {
	n := len(g.Blocks)
	info := &cfgInfo{irreducible: make([]bool, n)}
	if n == 0 {
		return info
	}

	// Predecessors, for the dominator dataflow.
	preds := make([][]int32, n)
	for _, b := range g.Blocks {
		for _, s := range b.Succs {
			preds[s.Index] = append(preds[s.Index], b.Index)
		}
	}

	// Dominator sets as bitsets: dom[b] = {b} ∪ ⋂ dom[preds(b)], with dom[entry] = {entry}.
	// CFGs are small, so the simple iterative dataflow converges in a few passes; no need for
	// Lengauer-Tarjan. Unreachable blocks keep "full" sets, which is fine: the structural DFS
	// below never reaches them, so their entries are never consulted.
	words := (n + 63) / 64
	full := make([]uint64, words)
	for i := range full {
		full[i] = ^uint64(0)
	}
	dom := make([][]uint64, n)
	for i := range dom {
		dom[i] = slices.Clone(full)
	}
	entry := int(g.Blocks[0].Index)
	for i := range dom[entry] {
		dom[entry][i] = 0
	}
	dom[entry][entry/64] |= 1 << (entry % 64)

	for changed := true; changed; {
		changed = false
		for _, b := range g.Blocks {
			i := int(b.Index)
			if i == entry {
				continue
			}
			var meet []uint64
			for _, p := range preds[i] {
				if meet == nil {
					meet = slices.Clone(dom[p])
				} else {
					for w := range meet {
						meet[w] &= dom[p][w]
					}
				}
			}
			if meet == nil {
				continue // Unreachable.
			}
			meet[i/64] |= 1 << (i % 64)
			if !slices.Equal(meet, dom[i]) {
				dom[i] = meet
				changed = true
			}
		}
	}
	dominates := func(v, u int32) bool {
		return dom[u][int(v)/64]&(1<<(int(v)%64)) != 0
	}

	// Tarjan's SCC algorithm.
	const unvisited = -1
	sccID := make([]int, n)
	index := make([]int, n)
	low := make([]int, n)
	onStack := make([]bool, n)
	for i := range index {
		index[i] = unvisited
		sccID[i] = unvisited
	}
	var stack []int32
	var counter, nSCCs int
	var strongconnect func(v int32)
	strongconnect = func(v int32) {
		index[v] = counter
		low[v] = counter
		counter++
		stack = append(stack, v)
		onStack[v] = true
		for _, s := range g.Blocks[v].Succs {
			w := s.Index
			if index[w] == unvisited {
				strongconnect(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				sccID[w] = nSCCs
				if w == v {
					break
				}
			}
			nSCCs++
		}
	}
	for _, b := range g.Blocks {
		if index[b.Index] == unvisited {
			strongconnect(b.Index)
		}
	}

	// One structural DFS from the entry; a retreating edge d→e (e on the DFS stack) that is not
	// a back edge (¬ e dom d) marks its SCC irreducible. A retreating edge closes a cycle, so d
	// and e share an SCC; marking both is a belt-and-suspenders guard for that invariant.
	irreducibleSCC := make([]bool, nSCCs)
	const (
		white   = 0
		onPath  = 1
		visited = 2
	)
	color := make([]int8, n)
	var dfs func(v int32)
	dfs = func(v int32) {
		color[v] = onPath
		for _, s := range g.Blocks[v].Succs {
			w := s.Index
			switch color[w] {
			case white:
				dfs(w)
			case onPath: // Retreating edge v→w.
				if !dominates(w, v) {
					irreducibleSCC[sccID[v]] = true
					irreducibleSCC[sccID[w]] = true
				}
			}
		}
		color[v] = visited
	}
	dfs(g.Blocks[0].Index)

	for _, b := range g.Blocks {
		info.irreducible[b.Index] = irreducibleSCC[sccID[b.Index]]
	}
	return info
}

// fingerprint canonically serializes the analysis state a block is entered with: the lock tree's
// nonzero nodes (sorted by encoded object path, with hold counters and acquire position), the
// pending deferred calls of the current defer scope in LIFO order (annotation-injected unlocks by
// path code, user defers by syntactic position), and the wrapper allowances. Two states
// fingerprint equally iff the analysis cannot distinguish them: zero-count tree nodes are pruned
// so that syntactically different but semantically equal trees collide.
func (l *lockAnalyzer) fingerprint(scope *lockScope) string {
	var entries []string
	var walk func(nd *node, path canonicalPath)
	walk = func(nd *node, path canonicalPath) {
		for obj, child := range nd.children {
			childPath := copyAppend(path, obj)
			if child.lockCount != 0 || child.rLockCount != 0 ||
				child.certainLockCount != 0 || child.certainRLockCount != 0 {
				entries = append(entries,
					l.cpCoder.encode(childPath)+":"+
						strconv.Itoa(child.lockCount)+","+
						strconv.Itoa(child.rLockCount)+","+
						strconv.Itoa(child.certainLockCount)+","+
						strconv.Itoa(child.certainRLockCount)+"@"+
						strconv.Itoa(int(child.acquirePos)))
			}
			walk(child, childPath)
		}
	}
	walk(scope.tree.root, nil)
	slices.Sort(entries) // The children map iterates in arbitrary order.

	var sb strings.Builder
	sb.WriteString("L:")
	sb.WriteString(strings.Join(entries, ";"))
	sb.WriteString("|D:")
	for _, call := range l.currentDeferred() {
		switch call := call.(type) {
		case canonicalPath:
			sb.WriteString("p")
			sb.WriteString(l.cpCoder.encode(call))
		case *ast.CallExpr:
			sb.WriteString("e")
			sb.WriteString(strconv.Itoa(int(call.Pos())))
		}
		sb.WriteString(";")
	}
	sb.WriteString("|A:")
	sb.WriteString(strconv.Itoa(scope.leakAllowance))
	if scope.leakAllowanceRLock {
		sb.WriteString(",r,")
	} else {
		sb.WriteString(",w,")
	}
	sb.WriteString(strconv.Itoa(scope.invalidReleaseAllowance))
	return sb.String()
}

// memoKey identifies one (block, entry-state) visit within a single analyzeCfg invocation.
type memoKey struct {
	block *cfg.Block
	fp    string
}