package lockguard

import (
	"fmt"
	"go/token"
	"go/types"
	"slices"
	"strings"
)

// TODO document the algorithm, and why we do canonicalization, etc.

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

type (
	lockResult interface {
		isUncertain() bool

		isRLock() bool
	}

	baseLockResult struct {
		uncertain bool
		rlock     bool
	}

	acquiredLockResult struct {
		baseLockResult
		deadlock bool
	}

	releasedLockResult struct {
		baseLockResult
		invalid bool
	}

	leakResult struct {
		baseLockResult
		path       canonicalPath
		acquirePos token.Pos
	}
)

func (r baseLockResult) isRLock() bool {
	return r.rlock
}

func (r baseLockResult) isUncertain() bool {
	return r.uncertain
}

type lockScope struct {
	tree lockTree

	// Lock-wrapper allowances. A type that implements sync.Locker (or the RW variant) exposes
	// Lock/Unlock methods whose whole job is to acquire or release a lock across the call boundary:
	// Lock() leaves a lock held at return (looks like a leak) and Unlock() releases a lock that was
	// never taken in this frame (looks like an invalid unlock). When analyzing such a method we grant
	// a one-shot allowance so that single expected imbalance is not reported. The fields live on the
	// scope so they fork per DFS path along with the lock state.
	leakAllowance           int  // suppress this many still-held locks at exit (Lock/RLock methods)
	leakAllowanceRLock      bool // the allowed leak is a read lock (RLock) rather than a write lock (Lock)
	invalidReleaseAllowance int  // treat this many not-held releases as valid (Unlock/RUnlock methods)
}

func newLockScope() *lockScope {
	return &lockScope{
		tree: lockTree{root: newNode(nil)},
	}
}

// fork returns an independent deep copy of this scope.
func (s *lockScope) fork() *lockScope {
	return &lockScope{
		tree:                    lockTree{root: s.tree.root.copy()},
		leakAllowance:           s.leakAllowance,
		leakAllowanceRLock:      s.leakAllowanceRLock,
		invalidReleaseAllowance: s.invalidReleaseAllowance,
	}
}

// detachOwned returns an independent copy of this scope with the roots owned by the predicate
// removed. It is used at the exit of an inline function literal to drop locks on variables declared
// inside the literal (out of scope in the caller, and already leak-checked at the literal), while
// lock state on enclosing-scope roots is preserved and flows onward into the caller.
func (s *lockScope) detachOwned(owned func(types.Object) bool) *lockScope {
	ns := s.fork()
	for obj, child := range ns.tree.root.children {
		if owned(child.obj) {
			delete(ns.tree.root.children, obj)
		}
	}
	return ns
}

func (s *lockScope) apply(path canonicalPath, pos token.Pos) lockResult {
	if !isLockOpPath(path) {
		return nil
	}

	switch op := lockOpOf(path[len(path)-1].Name()); op {
	case lockLockOp:
		return s.lock(path[:len(path)-1], false, pos)
	case rLockLockOp:
		return s.lock(path[:len(path)-1], true, pos)
	case unlockLockOp:
		return s.unlock(path[:len(path)-1], false)
	case rUnlockLockOp:
		return s.unlock(path[:len(path)-1], true)
	default:
		panic("should've been checked by isLockOpPath to not be the case")
	}
}

func (s *lockScope) lock(path canonicalPath, isRLock bool, pos token.Pos) acquiredLockResult {
	var funcName string
	if isRLock {
		funcName = "RLock"
	} else {
		funcName = "Lock"
	}

	lockFuncPath := locateFromObjByName(path[len(path)-1], funcName)
	if lockFuncPath == nil {
		panic(fmt.Sprintf("expected '%s' to locate a lock", path))
	}

	pathWithLockFunc := copyAppend(path, lockFuncPath...)
	treePath := s.tree.add(path)
	result := acquiredLockResult{
		baseLockResult: baseLockResult{
			uncertain: false,
			rlock:     isRLock,
		},
		deadlock: false,
	}
	for i := len(path) - 1; i >= 0; i-- {
		pathFromObj := locateFromObjByName(path[i], funcName)
		if pathFromObj == nil {
			return result
		}
		if !slices.Equal(pathWithLockFunc[i+1:], pathFromObj) {
			return result
		}

		kind := lockKindOfObject(path[i])
		if kind == noneLockKind {
			continue
		}

		nd := treePath[i]
		if !isRLock && (kind == normalLockKind || kind == rwLockKind) {
			if nd.certainLockCount >= 1 || nd.certainRLockCount >= 1 {
				result.deadlock = true
			} else if nd.lockCount >= 1 || nd.rLockCount >= 1 {
				result.deadlock = true
				result.uncertain = true
			}
			nd.lockCount++
			nd.certainLockCount++
			nd.acquirePos = pos
		} else if isRLock && kind == rwLockKind {
			if nd.certainLockCount >= 1 {
				result.deadlock = true
			} else if nd.lockCount >= 1 {
				result.deadlock = true
				result.uncertain = true
			}
			nd.rLockCount++
			nd.certainRLockCount++
			nd.acquirePos = pos
		}

		switch obj := path[i].(type) {
		case *types.Var:
			if !obj.Embedded() {
				return result
			}
		default:
			return result
		}
	}
	return result
}

func (s *lockScope) lockUncertain(path canonicalPath, isRLock bool, pos token.Pos) acquiredLockResult {
	var funcName string
	if isRLock {
		funcName = "RLock"
	} else {
		funcName = "Lock"
	}

	lockFuncPath := locateFromObjByName(path[len(path)-1], funcName)
	if lockFuncPath == nil {
		panic(fmt.Sprintf("expected '%s' to locate a lock", path))
	}

	pathWithLockFunc := copyAppend(path, lockFuncPath...)
	treePath := s.tree.add(path)
	result := acquiredLockResult{
		baseLockResult: baseLockResult{
			rlock: isRLock,
		},
		deadlock: false,
	}
	for i := len(path) - 1; i >= 0; i-- {
		pathFromObj := locateFromObjByName(path[i], funcName)
		if pathFromObj == nil {
			return result
		}
		if !slices.Equal(pathWithLockFunc[i+1:], pathFromObj) {
			return result
		}

		kind := lockKindOfObject(path[i])
		if kind == noneLockKind {
			continue
		}

		nd := treePath[i]
		if !isRLock && (kind == normalLockKind || kind == rwLockKind) {
			if nd.certainLockCount >= 1 || nd.certainRLockCount >= 1 {
				result.deadlock = true
			} else if nd.lockCount >= 1 || nd.rLockCount >= 1 {
				result.deadlock = true
				result.uncertain = true
			}
			nd.lockCount++
			nd.acquirePos = pos
		} else if isRLock && kind == rwLockKind {
			if nd.certainLockCount >= 1 {
				result.deadlock = true
			} else if nd.lockCount >= 1 {
				result.deadlock = true
				result.uncertain = true
			}
			nd.rLockCount++
			nd.acquirePos = pos
		}

		switch obj := path[i].(type) {
		case *types.Var:
			if !obj.Embedded() {
				return result
			}
		default:
			return result
		}
	}
	return result
}

func (s *lockScope) unlock(path canonicalPath, isRLock bool) releasedLockResult {
	treePath := s.tree.follow(path)

	if len(treePath) != len(path) {
		// The lock isn't held anywhere in this frame. Normally an invalid release, but an unlocking
		// method of a Locker type is allowed one such release (it unlocks on the caller's behalf).
		invalid := true
		if s.invalidReleaseAllowance > 0 {
			s.invalidReleaseAllowance--
			invalid = false
		}
		return releasedLockResult{
			baseLockResult: baseLockResult{
				uncertain: false,
				rlock:     isRLock,
			},
			invalid: invalid,
		}
	}

	var funcName string
	if isRLock {
		funcName = "RUnlock"
	} else {
		funcName = "Unlock"
	}

	unlockFuncPath := locateFromObjByName(path[len(path)-1], funcName)
	if unlockFuncPath == nil {
		panic(fmt.Sprintf("expected '%s' to locate a lock", path))
	}

	pathWithUnlockFunc := copyAppend(path, unlockFuncPath...)
	var result releasedLockResult
	result.rlock = isRLock
	var resultSet bool
	for i := len(path) - 1; i >= 0; i-- {
		pathFromObj := locateFromObjByName(path[i], funcName)
		if pathFromObj == nil {
			return result
		}
		if !slices.Equal(pathWithUnlockFunc[i+1:], pathFromObj) {
			return result
		}

		kind := lockKindOfObject(path[i])
		if kind == noneLockKind {
			continue
		}

		nd := treePath[i]
		localResult := releasedLockResult{
			baseLockResult: baseLockResult{
				rlock: isRLock,
			},
		}
		if !isRLock {
			if nd.certainLockCount <= 0 {
				localResult.invalid = true
				if nd.lockCount > 0 { // Unlock is only valid on some paths.
					localResult.uncertain = true
					nd.lockCount--
				}
			} else {
				nd.certainLockCount--
				nd.lockCount--
			}
		} else if nd.certainRLockCount <= 0 {
			localResult.invalid = true
			if nd.rLockCount > 0 { // RUnlock is only valid on some paths.
				localResult.uncertain = true
				nd.rLockCount--
			}
		} else {
			nd.certainRLockCount--
			nd.rLockCount--
		}

		if !resultSet {
			result = localResult
			resultSet = true
		} else if result != localResult {
			panic(fmt.Sprintf("expected %v to be equal to %v (different lockResults on the same lock path", localResult, result))
		}

		switch obj := path[i].(type) {
		case *types.Var:
			if !obj.Embedded() {
				return result
			}
		default:
			return result
		}
	}
	return result
}

// closePath returns the locks still held at this exit point that the owned predicate selects
// used for leak detection. The predicate decides which roots (first-level variables) belong to the
// exiting function: a top-level function owns everything, while an inline function literal owns only the
// variables declared inside it. This isolates leak detection for inline literals; they inherit the
// enclosing held-lock state (so protection checks still pass) but only report leaks on their own
// variables, not locks held by the enclosing function (which flow onward and are reported there).
func (s *lockScope) closePath(owned func(types.Object) bool) []leakResult {
	if s.tree.root == nil {
		return nil
	}

	var leaks []leakResult
	for _, child := range s.tree.root.children {
		if owned(child.obj) {
			leaks = append(leaks, collectHeldLocks(child, nil)...)
		}
	}

	// A locking method of a Locker type (e.g. (*RWMutex).Lock) is expected to leave one lock held at
	// return. Drop that single expected leak, preferring one whose kind (read vs write) matches the
	// method so an RLock method doesn't consume a write-lock leak and vice versa.
	for allowance := s.leakAllowance; allowance > 0 && len(leaks) > 0; allowance-- {
		idx := slices.IndexFunc(leaks, func(l leakResult) bool { return l.rlock == s.leakAllowanceRLock })
		if idx < 0 {
			idx = 0
		}
		leaks = slices.Delete(leaks, idx, idx+1)
	}

	return leaks
}

type missingProtection struct {
	protection
	uncertain bool
}

func (s *lockScope) checkProtections(path canonicalPath, access accessKind, prots []protection) (missingProtections []missingProtection) {
	if len(path) == 0 {
		return nil
	}

	for _, prot := range prots {
		lockPath := copyAppend(path[:len(path)-1], prot.lockPath...)
		treePath := s.tree.follow(lockPath)
		if len(lockPath) != len(treePath) {
			missingProtections = append(missingProtections, missingProtection{
				protection: prot,
				uncertain:  false,
			})
		} else {
			// TODO, we might want to add more reasoning as to why the protection is missing, and why it is not missing
			//       but it is uncertain.
			nd := treePath[len(treePath)-1]
			if nd.lockCount <= 0 && nd.rLockCount <= 0 {
				// Lock is not even possibly held.
				missingProtections = append(missingProtections, missingProtection{
					protection: prot,
					uncertain:  false,
				})
			} else if isRLock := nd.lockCount == 0; !prot.directive.isSatisfiedBy(lockKindOfObject(nd.obj), isRLock, access) {
				// Lock is possibly held, but this possibility doesn't satisfy the directive.
				missingProtections = append(missingProtections, missingProtection{
					protection: prot,
					uncertain:  false,
				})
			} else if nd.certainLockCount <= 0 && nd.certainRLockCount <= 0 {
				// Lock is possibly held, and this possibility satisfies the directive, but there is no certainty in that
				// possibility, therefore the protection is possibly missing.
				missingProtections = append(missingProtections, missingProtection{
					protection: prot,
					uncertain:  true,
				})
			} else if !prot.directive.isSatisfiedBy(lockKindOfObject(nd.obj), nd.certainLockCount == 0, access) {
				// Lock is possibly held, this possibility satisfies the directive, and there is certainty in that
				// possibility, but that certainty doesn't satisfy the directive, therefore the protection is
				// possibly missing.
				missingProtections = append(missingProtections, missingProtection{
					protection: prot,
					uncertain:  true,
				})
			}
		}
	}
	return
}

func formatLockNames(names []string) string {
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

	// Check there's at least one Lock or RLock node from the end and that lockOp is transferable through the
	// remaining suffix to that node.
	for i := len(path) - 2; i >= 0; i-- {
		pathToOp := locateFromObjByName(path[i], op.funcName())
		if !slices.Equal(path[i+1:], pathToOp) {
			return false // Locker's `op.funcName()` doesn't follow the same path given to us.
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
			next = newNode(obj)
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

		fmt.Printf("%v - <%d, %d, %d, %d>\n", n.obj, n.certainLockCount, n.certainRLockCount, n.lockCount, n.rLockCount)
		for obj, c := range n.children {
			fmt.Printf("%v -> %v\n", n.obj, obj)
			q = append(q, c)
		}
		fmt.Println()
	}
}

func (s *lockScope) print() {
	s.tree.print()
	fmt.Println()
}

type node struct {
	children map[types.Object]*node
	obj      types.Object

	// Counts for the number of times the lock is held, either certainly or only possibly (e.g. an uncertain TryLock).
	// In correct code, either both counts are zero, or lockCount is 1 and rLockCount is 0, or lockCount is 0 and
	// rLockCount is > 0. Otherwise, we'll generate warnings.
	lockCount, rLockCount int

	// The subset of lockCount and rLockCount that is certain.
	certainLockCount, certainRLockCount int

	acquirePos token.Pos
}

func newNode(obj types.Object) *node {
	return &node{
		children: make(map[types.Object]*node),
		obj:      obj,
	}
}

func (n *node) copy() *node {
	dst := newNode(n.obj)
	dst.certainLockCount, dst.certainRLockCount, dst.lockCount, dst.rLockCount =
		n.certainLockCount, n.certainRLockCount, n.lockCount, n.rLockCount
	dst.acquirePos = n.acquirePos
	for obj, child := range n.children {
		dst.children[obj] = child.copy()
	}
	return dst
}

func collectHeldLocks(root *node, path canonicalPath) (leaks []leakResult) {
	if root.obj != nil {
		path = append(path, root.obj)
		if lockKindOfObject(root.obj) != noneLockKind && root.lockCount+root.rLockCount > 0 {
			if root.lockCount > 0 {
				leaks = append(leaks, leakResult{
					baseLockResult: baseLockResult{
						uncertain: root.certainLockCount == 0,
						rlock:     false,
					},
					path:       path,
					acquirePos: root.acquirePos,
				})
			}

			if root.rLockCount > 0 {
				leaks = append(leaks, leakResult{
					baseLockResult: baseLockResult{
						uncertain: root.certainRLockCount == 0,
						rlock:     true,
					},
					path:       path,
					acquirePos: root.acquirePos,
				})
			}
		}
	}

	for _, child := range root.children {
		leaks = append(leaks, collectHeldLocks(child, path)...)
	}
	return
}
