package lockgaurd

import (
	"go/types"
	"slices"

	"golang.org/x/tools/go/analysis"
)

type lockState = int

const (
	unlockedLockState lockState = iota
	lockedLockState
	rLockedLockState
)

type lockOp int

const (
	noneLockOp lockOp = iota
	lockLockOp
	unlockLockOp
	rLockLockOp
	rUnlockLockOp
)

var lockOps = []lockOp{lockLockOp, unlockLockOp, rLockLockOp, rUnlockLockOp}

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
	for _, op := range lockOps {
		if name == op.funcName() {
			return op
		}
	}
	return noneLockOp
}

type lockScope struct {
	tree        lockTree
	deferredOps []canonicalPath
	pass        *analysis.Pass
}

func newLockScope(pass *analysis.Pass) *lockScope {
	return &lockScope{
		tree: lockTree{
			node: node{
				children: make(map[types.Object]*node),
				obj:      nil, // nil identifies root (global scope).
				state:    unlockedLockState,
			},
		},
		pass: pass,
	}
}

func (s *lockScope) apply(path canonicalPath) {
	if !isLockOpPath(path) {
		return
	}

	switch op := lockOpOf(path[len(path)-1].Name()); op {
	case lockLockOp, rLockLockOp:
		treePath := s.tree.add(path)
		for i := len(path) - 2; i >= 0; i-- {
			// Check if the locking/unlocking function is defined (directly or indirectly through embedded fields) by
			// this object.
			pathFromObj := locateFromObjByName(path[i], op.funcName(), true)
			if pathFromObj == nil {
				return
			}
			if !slices.Equal(path[i+1:], pathFromObj) {
				return // Op is not transferable.
			}

			kind := lockKindOfObject(path[i])
			if kind == noneLockKind {
				continue
			}

			// TODO warn about double-locking.
			if op == lockLockOp && (kind == normalLockKind || kind == rwLockKind) {
				treePath[i].state = lockedLockState
			} else if op == rLockLockOp && kind == rwLockKind {
				treePath[i].state = rLockedLockState
			}

			// Break if lock state is not transferable upwards.
			switch obj := path[i].(type) {
			case *types.Var:
				if !obj.Embedded() {
					return
				}
			case *types.Func, *types.PkgName:
				return
			}
		}
	case unlockLockOp, rUnlockLockOp:
		treePath := s.tree.follow(path)
		if len(treePath) == 0 {
			// TODO warn about unlocking a non-locked lock.
			return
		}

		for i := len(path) - 2; i >= 0; i-- {
			// Check if the locking/unlocking function is defined (directly or indirectly through embedded fields) by
			// this object's type.
			pathFromObj := locateFromObjByName(path[i], op.funcName(), true)
			if pathFromObj == nil {
				return
			}
			if !slices.Equal(path[i+1:], pathFromObj) {
				return // Op is not transferable.
			}

			kind := lockKindOfObject(path[i])
			if kind == noneLockKind {
				continue
			}

			if i < len(treePath) {
				if (treePath[i].state == lockedLockState && op == unlockLockOp) ||
					(treePath[i].state == rLockedLockState && op == rUnlockLockOp) {
					treePath[i].state = unlockedLockState
				}
			}

			// Break if lock state is not transferable upwards.
			switch obj := path[i].(type) {
			case *types.Var:
				if !obj.Embedded() {
					return
				}
			case *types.Func, *types.PkgName:
				return
			}
		}
	case noneLockOp:
		panic("should've been checked by isLockOpPath to not be the case")
	}
	return
}

func (s *lockScope) applyDeferred(path canonicalPath) {
	if !isLockOpPath(path) {
		return
	}
	s.deferredOps = append(s.deferredOps, path)
}

func (s *lockScope) flushDeferred() {
	for _, path := range s.deferredOps {
		s.apply(path)
	}
	s.deferredOps = nil
}

func (s *lockScope) missedProtections(objectPath canonicalPath, prots []protection, access accessKind) []protection {
	if len(objectPath) == 0 {
		return nil // Vacuously
	}

	var missedProts []protection
	for _, prot := range prots {
		lockPath := copyAppend(objectPath[:len(objectPath)-1], prot.lockPath...)
		treePath := s.tree.follow(lockPath)
		if len(lockPath) != len(treePath) {
			missedProts = append(missedProts, prot)
			continue
		}

		nd := treePath[len(treePath)-1]
		state := nd.state
		if state == unlockedLockState {
			missedProts = append(missedProts, prot)
			continue
		}

		if !prot.directive.isSatisfiedBy(lockKindOfObject(nd.obj), state == rLockedLockState, access) {
			missedProts = append(missedProts, prot)
			continue
		}
	}
	return missedProts
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
		pathToOp := locateFromObjByName(path[i], op.funcName(), true)
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
	node
}

func (t *lockTree) add(path canonicalPath) []*node {
	curr := &t.node
	var treePath []*node
	for _, obj := range path {
		if next, ok := curr.children[obj]; ok {
			curr = next
		} else {
			next = &node{
				children: make(map[types.Object]*node),
				obj:      obj,
				state:    unlockedLockState,
			}
			curr.children[obj] = next
			curr = next
		}
		treePath = append(treePath, curr)
	}
	return treePath
}

func (t *lockTree) follow(path canonicalPath) []*node {
	curr := &t.node
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

type node struct {
	children map[types.Object]*node
	obj      types.Object // Node object, nil for root.
	state    lockState
}
