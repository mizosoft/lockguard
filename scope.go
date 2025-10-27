package lockgaurd

import (
	"fmt"
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
	switch o {
	case lockLockOp:
		return "Lock"
	case unlockLockOp:
		return "Unlock"
	case rLockLockOp:
		return "RLock"
	case rUnlockLockOp:
		return "RUnlock"
	default:
		panic(fmt.Sprintf("unknown op: %d", o))
	}
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

type deferredOp struct {
	lockPath canonicalPath
	op       lockOp
}

type lockScope struct {
	tree       lockTree
	deferredOp []deferredOp
	pass       *analysis.Pass
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

func (s *lockScope) apply(lockPath canonicalPath, op lockOp) {
	if !isLockPath(lockPath, op) {
		return
	}

	switch op {
	case noneLockOp: // Skip
	case lockLockOp, rLockLockOp:
		treePath := s.tree.add(lockPath)
		for i := len(lockPath) - 1; i >= 0; i-- {
			// Check if the locking/unlocking function is defined (directly or indirectly through embedded fields) by
			// this object's type.
			context, contextDef := typeOf(lockPath[i])
			opFunc, pathFromObj := findFuncWithPath(context, contextDef, op.funcName())
			if pathFromObj == nil {
				return
			}
			if !slices.Equal(append(lockPath[i+1:], opFunc), pathFromObj) {
				return // Op is not transferable.
			}

			kind := lockKindOfObject(lockPath[i])
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
			switch obj := lockPath[i].(type) {
			case *types.Var:
				if !obj.Embedded() {
					return
				}
			case *types.Func:
				return
			}
		}
	case unlockLockOp, rUnlockLockOp:
		treePath := s.tree.follow(lockPath)
		if len(treePath) == 0 {
			// TODO warn about unlocking a non-locked lock.
			return
		}

		for i := len(lockPath) - 1; i >= 0; i-- {
			// Check if the locking/unlocking function is defined (directly or indirectly through embedded fields) by
			// this object's type.
			context, contextDef := typeOf(lockPath[i])
			opFunc, pathFromObj := findFuncWithPath(context, contextDef, op.funcName())
			if pathFromObj == nil {
				return
			}
			if !slices.Equal(append(lockPath[i+1:], opFunc), pathFromObj) {
				return
			}

			kind := lockKindOfObject(lockPath[i])
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
			switch obj := lockPath[i].(type) {
			case *types.Var:
				if !obj.Embedded() {
					return
				}
			case *types.Func:
				return
			}
		}
	}
	return
}

func (s *lockScope) applyDeferred(lockPath canonicalPath, unlockOp lockOp) {
	if !isLockPath(lockPath, unlockOp) {
		return
	}
	s.deferredOp = append(s.deferredOp, deferredOp{lockPath, unlockOp})
}

func (s *lockScope) flushDeferred() {
	for _, entry := range s.deferredOp {
		s.apply(entry.lockPath, entry.op)
	}
	s.deferredOp = nil
}

func (s *lockScope) isProtected(objectPath canonicalPath, prots []protection, access accessKind) bool {
	for _, prot := range prots {
		lockPath := append(objectPath[:len(objectPath)-1], prot.lockPath...)
		treePath := s.tree.follow(lockPath)
		if len(treePath) != len(lockPath) {
			return false
		}

		nd := treePath[len(treePath)-1]
		state := nd.state
		if state == unlockedLockState {
			return false
		}

		if !prot.directive.isSatisfiedBy(lockKindOfObject(nd.obj), state == rLockedLockState, access) {
			return false
		}
	}
	return true
}

func isLockPath(lockPath canonicalPath, op lockOp) bool {
	if len(lockPath) == 0 {
		return false
	}

	// Check there's at least one Lock or RLock node from the end and that op is transferable through the
	// remaining suffix.
	for i := len(lockPath) - 1; i >= 0; i-- {
		context, contextDef := typeOf(lockPath[len(lockPath)-1])
		opFunc, pathToOp := findFuncWithPath(context, contextDef, op.funcName())
		if !slices.Equal(append(lockPath[i+1:], opFunc), pathToOp) {
			return false
		}

		kind := lockKindOfObject(lockPath[i])
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
