package lockguard

import (
	"fmt"
	"go/types"
	"slices"
	"strings"

	"golang.org/x/tools/go/cfg"
)

type lockOp int

const (
	noneLockOp lockOp = iota
	lockLockOp
	unlockLockOp
	rLockLockOp
	rUnlockLockOp
)

func (o lockOp) funcName() string {
	return [...]string{
		noneLockOp:    "<invalid>",
		lockLockOp:    "Lock",
		unlockLockOp:  "Unlock",
		rLockLockOp:   "RLock",
		rUnlockLockOp: "RUnlock",
	}[o]
}

func (o lockOp) String() string {
	return o.funcName()
}

func lockOpOf(name string) lockOp {
	for _, op := range []lockOp{lockLockOp, unlockLockOp, rLockLockOp, rUnlockLockOp} {
		if name == op.funcName() {
			return op
		}
	}
	return noneLockOp
}

type deferredOp struct {
	block *cfg.Block
	path  canonicalPath
}

type lockScope struct {
	trees       map[*cfg.Block]lockTree // Map's from CFG block IDs to the corresponding lockTree identifying lock states.
	deferredOps []deferredOp
}

func newLockScope() *lockScope {
	return &lockScope{
		trees: make(map[*cfg.Block]lockTree),
	}
}

func (s *lockScope) apply(block *cfg.Block, path canonicalPath) []lockDiagnostic {
	if !isLockOpPath(path) {
		return nil
	}

	switch op := lockOpOf(path[len(path)-1].Name()); op {
	case lockLockOp:
		return s.lock(block, path[:len(path)-1], false)
	case rLockLockOp:
		return s.lock(block, path[:len(path)-1], true)
	case unlockLockOp:
		return s.unlock(block, path[:len(path)-1], false)
	case rUnlockLockOp:
		return s.unlock(block, path[:len(path)-1], true)
	default:
		panic("should've been checked by isLockOpPath to not be the case")
	}
}

func (s *lockScope) lock(block *cfg.Block, path canonicalPath, isRLock bool) (warnings []lockDiagnostic) {
	tree, ok := s.trees[block]
	if !ok {
		tree = lockTree{newNode(nil)}
		s.trees[block] = tree
	}

	var funcName string
	if isRLock {
		funcName = "RLock"
	} else {
		funcName = "Lock"
	}

	pathWithLockFunc := copyAppend(path, locateFromObjByName(path[len(path)-1], funcName)...)

	treePath := tree.add(path)
	for i := len(path) - 1; i >= 0; i-- {
		// Check if the locking function is defined (directly or indirectly through embedded fields) by
		// this object.
		pathFromObj := locateFromObjByName(path[i], funcName)
		if pathFromObj == nil {
			return
		}
		if !slices.Equal(pathWithLockFunc[i+1:], pathFromObj) {
			return // Locking is not transferable as paths to the locking function starts diverging.
		}

		kind := lockKindOfObject(path[i])
		if kind == noneLockKind {
			continue // Locking will only activate when we reach a lock object.
		}

		nd := treePath[i]
		if !isRLock && (kind == normalLockKind || kind == rwLockKind) {
			if nd.certainLockCount >= 1 || nd.certainRLockCount >= 1 {
				warnings = append(warnings, lockDiagnostic{CategoryDeadlock, fmt.Sprintf("acquiring '%v' that is already held [deadlock]", nd.obj.Name())})
			} else if nd.possibleLockCount >= 1 || nd.possibleRLockCount >= 1 {
				warnings = append(warnings, lockDiagnostic{CategoryDeadlock, fmt.Sprintf("acquiring '%v' that may be held [deadlock]", nd.obj.Name())})
			}
			nd.certainLockCount++
			nd.possibleLockCount++
		} else if isRLock && kind == rwLockKind {
			if nd.certainLockCount >= 1 {
				warnings = append(warnings, lockDiagnostic{CategoryDeadlock, fmt.Sprintf("acquiring '%v' that is already held [deadlock]", nd.obj.Name())})
			} else if nd.possibleLockCount >= 1 {
				warnings = append(warnings, lockDiagnostic{CategoryDeadlock, fmt.Sprintf("acquiring '%v' that may be held [deadlock]", nd.obj.Name())})
			}
			nd.certainRLockCount++
			nd.possibleRLockCount++
		}

		// Break if lock state is not transferable upwards.
		switch obj := path[i].(type) {
		case *types.Var:
			if !obj.Embedded() {
				return
			}
		default:
			return
		}
	}
	return
}

func (s *lockScope) possibleLock(block *cfg.Block, path canonicalPath, isRLock bool) (warnings []lockDiagnostic) {
	tree, ok := s.trees[block]
	if !ok {
		tree = lockTree{newNode(nil)}
		s.trees[block] = tree
	}

	var funcName string
	if isRLock {
		funcName = "RLock"
	} else {
		funcName = "Lock"
	}

	pathWithLockFunc := copyAppend(path, locateFromObjByName(path[len(path)-1], funcName)...)

	treePath := tree.add(path)
	for i := len(path) - 1; i >= 0; i-- {
		// Check if the locking function is defined (directly or indirectly through embedded fields) by
		// this object.
		pathFromObj := locateFromObjByName(path[i], funcName)
		if pathFromObj == nil {
			return
		}
		if !slices.Equal(pathWithLockFunc[i+1:], pathFromObj) {
			return // Locking is not transferable as paths to the locking function starts diverging.
		}

		kind := lockKindOfObject(path[i])
		if kind == noneLockKind {
			continue // Locking will only activate when we reach a lock object.
		}

		nd := treePath[i]
		if !isRLock && (kind == normalLockKind || kind == rwLockKind) {
			if nd.certainLockCount >= 1 || nd.certainRLockCount >= 1 {
				warnings = append(warnings, lockDiagnostic{CategoryPossibleDeadlock, fmt.Sprintf("acquiring '%v' that is already held [possible deadlock]", nd.obj.Name())})
			} else if nd.possibleLockCount >= 1 || nd.possibleRLockCount >= 1 {
				warnings = append(warnings, lockDiagnostic{CategoryPossibleDeadlock, fmt.Sprintf("acquiring '%v' that may be held [possible deadlock]", nd.obj.Name())})
			}
			nd.possibleLockCount++
		} else if isRLock && kind == rwLockKind {
			if nd.certainLockCount >= 1 {
				warnings = append(warnings, lockDiagnostic{CategoryPossibleDeadlock, fmt.Sprintf("acquiring '%v' that is already held [possible deadlock]", nd.obj.Name())})
			} else if nd.possibleLockCount >= 1 {
				warnings = append(warnings, lockDiagnostic{CategoryPossibleDeadlock, fmt.Sprintf("acquiring '%v' that may be held [possible deadlock]", nd.obj.Name())})
			}
			nd.possibleRLockCount++
		}

		// Break if lock state is not transferable upwards.
		switch obj := path[i].(type) {
		case *types.Var:
			if !obj.Embedded() {
				return
			}
		default:
			return
		}
	}
	return
}

func (s *lockScope) unlock(block *cfg.Block, path canonicalPath, isRLock bool) (warnings []lockDiagnostic) {
	tree, ok := s.trees[block]

	var treePath []*node
	if ok {
		treePath = tree.follow(path)
	}

	if len(treePath) == 0 {
		if isRLock {
			warnings = append(warnings, lockDiagnostic{CategoryInvalidUnlock, fmt.Sprintf("releasing read lock on '%v' that is not held", path[len(path)-1].Name())})
		} else {
			warnings = append(warnings, lockDiagnostic{CategoryInvalidUnlock, fmt.Sprintf("releasing '%v' that is not held", path[len(path)-1].Name())})
		}
		return
	}

	var funcName string
	if isRLock {
		funcName = "RUnlock"
	} else {
		funcName = "Unlock"
	}

	pathWithUnlockFunc := copyAppend(path, locateFromObjByName(path[len(path)-1], funcName)...)

	for i := len(path) - 1; i >= 0; i-- {
		// Check if the locking function is defined (directly or indirectly through embedded fields) by
		// this object.
		pathFromObj := locateFromObjByName(path[i], funcName)
		if pathFromObj == nil {
			return
		}
		if !slices.Equal(pathWithUnlockFunc[i+1:], pathFromObj) {
			return // Unlocking is not transferable as paths to the unlocking function starts diverging.
		}

		kind := lockKindOfObject(path[i])
		if kind == noneLockKind {
			continue // Unlocking will only activate when we reach a lock object.
		}

		nd := treePath[i]
		if !isRLock {
			if nd.certainLockCount <= 0 {
				if nd.possibleLockCount > 0 {
					warnings = append(warnings, lockDiagnostic{CategoryInvalidUnlock, fmt.Sprintf("releasing '%v' that may not be held", nd.obj.Name())})
					nd.possibleLockCount--
				} else {
					warnings = append(warnings, lockDiagnostic{CategoryInvalidUnlock, fmt.Sprintf("releasing '%v' that is not held", nd.obj.Name())})
				}
			} else {
				nd.certainLockCount--
				nd.possibleLockCount--
			}
		} else if nd.certainRLockCount <= 0 {
			if nd.possibleRLockCount > 0 {
				warnings = append(warnings, lockDiagnostic{CategoryInvalidUnlock, fmt.Sprintf("releasing read lock on '%v' that may not be held", nd.obj.Name())})
				nd.possibleRLockCount--
			} else {
				warnings = append(warnings, lockDiagnostic{CategoryInvalidUnlock, fmt.Sprintf("releasing read lock on '%v' that is not held", nd.obj.Name())})
			}
		} else {
			nd.certainRLockCount--
			nd.possibleRLockCount--
		}

		// Break if lock state is not transferable upwards.
		switch obj := path[i].(type) {
		case *types.Var:
			if !obj.Embedded() {
				return
			}
		default:
			return
		}
	}
	return
}

func (s *lockScope) merge(src *cfg.Block, dst *cfg.Block) {
	srcTree := s.trees[src]
	if srcTree.root == nil {
		srcTree.root = newNode(nil) // Assume this is an empty tree.
	}

	if dstTree, ok := s.trees[dst]; ok {
		dstTree.root.mergeFrom(srcTree.root)
	} else {
		s.trees[dst] = lockTree{srcTree.root.copy()}
	}
}

func (s *lockScope) applyDeferred(entry *cfg.Block, path canonicalPath) {
	if !isLockOpPath(path) {
		return
	}
	s.deferredOps = append(s.deferredOps, deferredOp{entry, path})
}

func (s *lockScope) flushDeferred() {
	for _, entry := range s.deferredOps {
		s.apply(entry.block, entry.path)
	}
	s.deferredOps = nil
}

func (s *lockScope) checkProtections(source *cfg.Block, objectPath canonicalPath, prots []protection, access accessKind) (warnings []lockDiagnostic) {
	if len(objectPath) == 0 {
		return nil // Vacuously
	}

	var verb string
	if access == writeAccessKind {
		verb = "writing"
	} else {
		verb = "reading"
	}
	field := "'" + objectPath.String() + "'"

	// lockPaths resolves each protection's lock relative to the object's prefix
	// and returns the full dot-joined path strings (e.g. "s.mu", "b.GlobalMut").
	lockPaths := func(ps []protection) []string {
		names := make([]string, len(ps))
		for i, p := range ps {
			names[i] = canonicalPath(copyAppend(objectPath[:len(objectPath)-1], p.lockPath...)).String()
		}
		return names
	}

	tree, ok := s.trees[source]
	if !ok {
		return []lockDiagnostic{{CategoryMissingLock, fmt.Sprintf("%s %s requires holding %s", verb, field, formatLocks(lockPaths(prots)))}}
	}

	var missedProts []protection
	var possiblyMissedProts []protection
	for _, prot := range prots {
		lockPath := copyAppend(objectPath[:len(objectPath)-1], prot.lockPath...)
		treePath := tree.follow(lockPath)
		if len(lockPath) != len(treePath) {
			missedProts = append(missedProts, prot)
		} else if nd := treePath[len(treePath)-1]; (nd.possibleLockCount <= 0 && nd.possibleRLockCount <= 0) || !prot.directive.isSatisfiedBy(lockKindOfObject(nd.obj), nd.possibleLockCount == 0, access) {
			missedProts = append(missedProts, prot)
		} else if (nd.certainLockCount <= 0 && nd.certainRLockCount <= 0) || !prot.directive.isSatisfiedBy(lockKindOfObject(nd.obj), nd.certainLockCount == 0, access) {
			possiblyMissedProts = append(possiblyMissedProts, prot)
		}
	}

	if len(missedProts) > 0 {
		warnings = append(warnings, lockDiagnostic{CategoryMissingLock, fmt.Sprintf("%s %s requires holding %s", verb, field, formatLocks(lockPaths(missedProts)))})
	}
	if len(possiblyMissedProts) > 0 {
		warnings = append(warnings, lockDiagnostic{CategoryPossiblyMissingLock, fmt.Sprintf("%s %s requires holding %s (not held on all paths)", verb, field, formatLocks(lockPaths(possiblyMissedProts)))})
	}
	return
}

// formatLocks joins quoted lock names, e.g. "'mu'" or "'mu1' and 'mu2'" or "'mu1', 'mu2' and 'mu3'".
func formatLocks(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = "'" + name + "'"
	}
	switch len(quoted) {
	case 0:
		return ""
	case 1:
		return quoted[0]
	case 2:
		return quoted[0] + " and " + quoted[1]
	default:
		return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
	}
}

func isLockOpPath(path canonicalPath) bool {
	if len(path) <= 1 {
		return false
	}

	if _, isFunc := path[len(path)-1].(*types.Func); !isFunc {
		return false
	}

	op := lockOpOf(path[len(path)-1].Name())
	if op == noneLockOp {
		return false
	}

	// Check there's at least one Lock or RLock node from the end and that op is transferable through the
	// remaining suffix to that node.
	for i := len(path) - 2; i >= 0; i-- {
		pathToOp := locateFromObjByName(path[i], op.funcName())
		if !slices.Equal(path[i+1:], pathToOp) {
			return false
		}

		kind := lockKindOfObject(path[i])
		if kind != noneLockKind && (kind == rwLockKind || (op != rLockLockOp && op != rUnlockLockOp)) {
			return true
		}
	}
	return false
}

type lockTree struct {
	root *node
}

func (t lockTree) add(path canonicalPath) []*node {
	curr := t.root
	var treePath []*node
	for _, obj := range path {
		if next, ok := curr.children[obj]; ok {
			curr = next
		} else {
			next = &node{
				children: make(map[types.Object]*node),
				obj:      obj,
			}
			curr.children[obj] = next
			curr = next
		}
		treePath = append(treePath, curr)
	}
	return treePath
}

func (t lockTree) follow(path canonicalPath) []*node {
	curr := t.root
	var treePath []*node
	for _, obj := range path {
		if next, ok := curr.children[obj]; ok {
			treePath = append(treePath, next)
			curr = next
		} else {
			break
		}
	}
	return treePath
}

func (t lockTree) print() {
	q := make([]*node, 0)
	if t.root != nil {
		q = append(q, t.root)
	}

	for len(q) > 0 {
		n := q[0]
		q = q[1:]

		fmt.Printf("%v - <%d, %d, %d, %d>\n", n.obj, n.certainLockCount, n.certainRLockCount, n.possibleLockCount, n.possibleRLockCount)
		for obj, c := range n.children {
			fmt.Printf("%v -> %v\n", n.obj, obj)
			q = append(q, c)
		}
		fmt.Println()
	}
}

func (s *lockScope) print(block *cfg.Block) {
	s.trees[block].print()
	fmt.Println()
}

type node struct {
	children                              map[types.Object]*node
	obj                                   types.Object // Node object, nil for root.
	certainLockCount, certainRLockCount   int
	possibleLockCount, possibleRLockCount int
}

func newNode(obj types.Object) *node {
	return &node{
		children: make(map[types.Object]*node),
		obj:      obj,
	}
}

func (n *node) copy() *node {
	dst := newNode(n.obj)
	dst.certainLockCount, dst.certainRLockCount, dst.possibleLockCount, dst.possibleRLockCount =
		n.certainLockCount, n.certainRLockCount, n.possibleLockCount, n.possibleRLockCount
	for obj, child := range n.children {
		dst.children[obj] = child.copy()
	}
	return dst
}

func (n *node) mergeFrom(src *node) {
	if n.obj != src.obj {
		panic("merging nodes of different objects")
	}

	n.certainLockCount, n.certainRLockCount = min(n.certainLockCount, src.certainLockCount), min(n.certainRLockCount, src.certainRLockCount)
	n.possibleLockCount, n.possibleRLockCount = max(n.possibleLockCount, src.possibleLockCount), max(n.possibleRLockCount, src.possibleRLockCount)

	// Merge src children into dst.
	for obj, srcChild := range src.children {
		if dstChild, ok := n.children[obj]; ok {
			dstChild.mergeFrom(srcChild)
		} else {
			dstChild = newNode(srcChild.obj)
			dstChild.mergeFrom(srcChild)
			n.children[obj] = dstChild
		}
	}

	// Handle dst children that don't exist in src - merge with empty state.
	for obj, dstChild := range n.children {
		if _, exists := src.children[obj]; !exists {
			dstChild.mergeFrom(newNode(obj)) // Merge with empty node.
		}
	}
}
