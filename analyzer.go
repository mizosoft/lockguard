package lockgaurd

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// TODO handle struct literals.
// TODO we should handle facts exported from other packages.
// TODO we can also support once.Do patterns.

// Analyzer Checks lock-protected accesses.
var Analyzer = &analysis.Analyzer{
	Name:      "lockguard",
	Doc:       "Checks lock-protected accesses",
	Run:       run,
	Requires:  []*analysis.Analyzer{inspect.Analyzer},
	FactTypes: []analysis.Fact{new(protectionFact)},
}

func run(pass *analysis.Pass) (interface{}, error) {
	if pass.Pkg.Name() != "a" {
		return nil, nil
	}

	if lockerInterface == nil {
		return nil, fmt.Errorf("unable to find sync.Locker interface type")
	}

	ins := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	f := newFinder(pass)
	f.find(ins)

	l := &lockAnalyzer{
		protections: f.protections,
		stack:       make([]ast.Node, 0),
		pass:        pass,
	}
	ins.Preorder([]ast.Node{(*ast.FuncDecl)(nil), (*ast.GenDecl)(nil), (*ast.BadDecl)(nil)}, func(node ast.Node) {
		l.analyzeDecl(node.(ast.Decl))
	})

	return nil, nil
}

func nillOf[T any]() T {
	var t T
	return t
}

func ancestorAs[N ast.Node](l *lockAnalyzer, upDepth int) (N, bool) {
	ln := len(l.stack)
	if ln-upDepth-1 < 0 {
		return nillOf[N](), false
	}
	parent := l.stack[ln-upDepth-1]
	if exprParent, ok := parent.(ast.Expr); ok {
		parent = ast.Unparen(exprParent)
	}
	typedParent, ok := parent.(N)
	return typedParent, ok
}

func findRootIdent(expr ast.Expr) (*ast.Ident, bool) {
	switch expr := expr.(type) {
	case *ast.BadExpr:
		return nil, false
	case *ast.CallExpr:
		return findRootIdent(expr.Fun)
	case *ast.Ident:
		return expr, true
	case *ast.IndexExpr:
		return findRootIdent(expr.X)
	case *ast.ParenExpr:
		return findRootIdent(expr.X)
	case *ast.SelectorExpr:
		return findRootIdent(expr.X)
	case *ast.SliceExpr:
		return findRootIdent(expr.X)
	case *ast.StarExpr:
		return findRootIdent(expr.X)
	case *ast.UnaryExpr:
		return findRootIdent(expr.X)
	case nil:
		return nil, true
	default:
		return nil, false
	}
}

func findRootObj(expr ast.Expr, pass *analysis.Pass) (types.Object, bool) {
	if rootIdent, ok := findRootIdent(expr); ok {
		if rootIdent == nil {
			return nil, true // Global context.
		} else if rootObj := pass.TypesInfo.ObjectOf(rootIdent); rootObj != nil {
			return rootObj, true
		}
	}
	return nil, false
}

type heldLock struct {
	isRead   bool
	kind     lockKind
	selector ast.Expr // The expression selecting the lock.
}

type lockScope struct {
	locks map[types.Object]map[types.Object][]heldLock // lockObj -> root selector (nil if global) -> list of expressions holding the lock.
}

func (s *lockScope) add(lockObj types.Object, root types.Object, lock heldLock) {
	locksForVar, ok := s.locks[lockObj]
	if !ok {
		locksForVar = make(map[types.Object][]heldLock)
		s.locks[lockObj] = locksForVar
	}
	locksForVar[root] = append(locksForVar[root], lock)
}

func (s *lockScope) remove(lockObj types.Object, root types.Object, lock heldLock) {
	edited := make([]heldLock, 0)
	if locksForVar, ok := s.locks[lockObj]; ok {
		for _, existingLock := range locksForVar[root] {
			if existingLock.isRead != lock.isRead || !expressionsMatch(existingLock.selector, lock.selector) {
				edited = append(edited, existingLock)
			}
		}

		if len(edited) > 0 {
			s.locks[lockObj][root] = edited
		} else {
			delete(s.locks[lockObj], root)
			if len(s.locks[lockObj]) == 0 {
				delete(s.locks, lockObj)
			}
		}
	}
}

func (s *lockScope) removeAll(lockObj types.Object, root types.Object, locks []heldLock) {
	edited := make([]heldLock, 0)
	if locksForVar, ok := s.locks[lockObj]; ok {
		for _, existingLock := range locksForVar[root] {
			add := true
			for _, lock := range locks {
				if existingLock.isRead != lock.isRead || !expressionsMatch(existingLock.selector, lock.selector) {
					add = false
					break
				}
			}

			if add {
				edited = append(edited, existingLock)
			}
		}

		if len(edited) > 0 {
			s.locks[lockObj][root] = edited
		} else {
			delete(s.locks[lockObj], root)
			if len(s.locks[lockObj]) == 0 {
				delete(s.locks, lockObj)
			}
		}
	}
}

func (s *lockScope) isProtectedBy(prot protection, expr ast.Expr, access accessKind, root types.Object) bool {
	if locksForVar, ok := s.locks[prot.lockObj]; ok {
		for _, lock := range locksForVar[root] {
			if prot.directive.isSatisfiedBy(lock, access) {
				if trimmedLockSelector, ok := trimSuffix(lock.selector, prot.lockExpr); ok && expressionsMatch(trimmedLockSelector, expr) {
					return true
				}
			}
		}
	}
	return false
}

func (s *lockScope) isProtectedByAll(prots []protection, expr ast.Expr, access accessKind, pass *analysis.Pass) bool {
	root, ok := findRootObj(expr, pass)
	if !ok {
		return false
	}

	for _, prot := range prots {
		if !s.isProtectedBy(prot, expr, access, root) {
			return false
		}
	}
	return true
}

type accessKind int

const (
	readAccessKind  accessKind = iota
	writeAccessKind            = iota
)

type lockAnalyzer struct {
	protections protectionsMap
	lockScopes  []*lockScope
	deferScopes []*lockScope
	stack       []ast.Node
	accessStack []accessKind
	pass        *analysis.Pass
}

func (l *lockAnalyzer) enterLockScope() {
	l.lockScopes = append(l.lockScopes, &lockScope{
		locks: make(map[types.Object]map[types.Object][]heldLock),
	})
}

func (l *lockAnalyzer) exitLockScope() {
	ln := len(l.lockScopes)
	l.lockScopes[ln-1] = nil
	l.lockScopes = l.lockScopes[0 : ln-1]
}

func (l *lockAnalyzer) enterDeferScope() {
	l.deferScopes = append(l.deferScopes, &lockScope{
		locks: make(map[types.Object]map[types.Object][]heldLock),
	})
}

func (l *lockAnalyzer) exitDeferScope() {
	ln := len(l.deferScopes)
	scope := l.deferScopes[ln-1]
	l.deferScopes[ln-1] = nil
	l.deferScopes = l.deferScopes[0 : ln-1]
	for lockObj, roots := range scope.locks {
		for root, locks := range roots {
			l.unlockAll(lockObj, root, locks)
		}
	}
}

func (l *lockAnalyzer) enterAccess(access accessKind) {
	l.accessStack = append(l.accessStack, access)
}

func (l *lockAnalyzer) leaveAccess() {
	l.accessStack = l.accessStack[0 : len(l.accessStack)-1]
}

func (l *lockAnalyzer) currentAccess() accessKind {
	ln := len(l.accessStack)
	if ln > 0 {
		return l.accessStack[ln-1]
	}
	return readAccessKind // Default to read access.
}

// TODO a wild idea: consider pointer assignment paths to check if we're referring to the same lock without
//      necessarily locking/unlocking it with the same expr.

func (l *lockAnalyzer) lock(lockObj types.Object, root types.Object, lock heldLock) {
	l.lockScopes[len(l.lockScopes)-1].add(lockObj, root, lock)
}

func (l *lockAnalyzer) unlock(lockObj types.Object, root types.Object, lock heldLock) {
	l.lockScopes[len(l.lockScopes)-1].remove(lockObj, root, lock)
}

func (l *lockAnalyzer) unlockAll(lockObj types.Object, root types.Object, locks []heldLock) {
	str := ""
	for _, l := range locks {
		str += types.ExprString(l.selector)
	}
	l.lockScopes[len(l.lockScopes)-1].removeAll(lockObj, root, locks)
}

func (l *lockAnalyzer) deferredUnlock(lockObj types.Object, root types.Object, lock heldLock) {
	ln := len(l.deferScopes)
	l.deferScopes[ln-1].add(lockObj, root, lock)
}

func (l *lockAnalyzer) analyzeDecl(decl ast.Decl) {
	l.enter(decl)
	defer l.leave()

	switch decl := decl.(type) {
	case *ast.FuncDecl:
		l.enterLockScope()
		l.enterDeferScope()

		// If this function is protected by a lock, we'll assume this lock is held while analyzing it. This allows other
		// functions/variables protected by the same lock to be called within this function.
		var explicitLocks []struct {
			prot protection
			recv *types.Var
		}
		if fnc, ok := l.pass.TypesInfo.ObjectOf(decl.Name).(*types.Func); ok {
			for _, prot := range l.protections.getAll(fnc) {
				if recv := fnc.Signature().Recv(); recv != nil {
					explicitLocks = append(explicitLocks, struct {
						prot protection
						recv *types.Var
					}{prot, recv})
					l.lock(prot.lockObj, recv, heldLock{
						kind:     lockKindOfObject(prot.lockObj),
						selector: prot.lockExprWithReceiver,
					})
				}
			}
		}

		l.analyzeStmt(decl.Body)

		for _, el := range explicitLocks {
			if el.recv != nil {
				l.unlock(el.prot.lockObj, el.recv, heldLock{
					kind:     lockKindOfObject(el.prot.lockObj),
					selector: el.prot.lockExprWithReceiver,
				})
			}
		}

		l.exitDeferScope()
		l.exitLockScope()
	case *ast.GenDecl:
		if decl.Tok == token.VAR {
			for _, spec := range decl.Specs {
				if valueSpec, isValueSpec := spec.(*ast.ValueSpec); isValueSpec {
					l.analyzeExprs(valueSpec.Values)
				}
			}
		}
	case *ast.BadDecl:
		// Skip
	}
}

// TODO handle global vars when we allow protection specs by comments.
func (l *lockAnalyzer) analyzeStmt(stmt ast.Stmt) {
	if stmt == nil {
		return
	}

	l.enter(stmt)
	defer l.leave()

	switch stmt := stmt.(type) {
	case *ast.DeclStmt:
		l.analyzeDecl(stmt.Decl)
	case *ast.LabeledStmt:
		l.analyzeStmt(stmt.Stmt)
	case *ast.ExprStmt:
		l.analyzeExpr(stmt.X)
	case *ast.SendStmt:
		l.enterAccess(writeAccessKind) // Sending to a channel is writing to it.
		l.analyzeExpr(stmt.Chan)
		l.leaveAccess()

		l.enterAccess(readAccessKind)
		l.analyzeExpr(stmt.Value)
		l.leaveAccess()
	case *ast.IncDecStmt:
		l.enterAccess(writeAccessKind)
		l.analyzeExpr(stmt.X)
		l.leaveAccess()
	case *ast.AssignStmt:
		l.enterAccess(writeAccessKind)
		l.analyzeExprs(stmt.Lhs)
		l.leaveAccess()

		l.enterAccess(readAccessKind)
		l.analyzeExprs(stmt.Rhs)
		l.leaveAccess()
	case *ast.GoStmt:
		// We're reading the function to launch a goroutine with.
		l.enterAccess(readAccessKind)
		l.analyzeExpr(stmt.Call)
		l.leaveAccess()
	case *ast.DeferStmt:
		// We're reading the function to defer.
		l.enterAccess(readAccessKind)
		l.analyzeExpr(stmt.Call)
		l.leaveAccess()
	case *ast.ReturnStmt:
		l.enterAccess(readAccessKind)
		l.analyzeExprs(stmt.Results)
		l.leaveAccess()
	case *ast.BlockStmt:
		for _, stmt := range stmt.List {
			l.analyzeStmt(stmt)
		}
	case *ast.IfStmt:
		l.analyzeStmt(stmt.Init)
		l.enterAccess(readAccessKind)
		l.analyzeExpr(stmt.Cond)
		l.leaveAccess()
		l.analyzeStmt(stmt.Body)
		l.analyzeStmt(stmt.Else)
	case *ast.CaseClause:
		l.enterAccess(readAccessKind)
		l.analyzeExprs(stmt.List)
		l.leaveAccess()
		for _, innerStmt := range stmt.Body {
			l.analyzeStmt(innerStmt)
		}
	case *ast.SwitchStmt:
		l.analyzeStmt(stmt.Init)
		l.enterAccess(readAccessKind)
		l.analyzeExpr(stmt.Tag)
		l.leaveAccess()
		l.analyzeStmt(stmt.Body)
	case *ast.TypeSwitchStmt:
		l.analyzeStmt(stmt.Init)
		l.analyzeStmt(stmt.Assign)
		l.analyzeStmt(stmt.Body)
	case *ast.CommClause:
		l.analyzeStmt(stmt.Comm)
		for _, innerStmt := range stmt.Body {
			l.analyzeStmt(innerStmt)
		}
	case *ast.SelectStmt:
		l.analyzeStmt(stmt.Body)
	case *ast.ForStmt:
		l.analyzeStmt(stmt.Init)
		l.enterAccess(readAccessKind)
		l.analyzeExpr(stmt.Cond)
		l.leaveAccess()
		l.analyzeStmt(stmt.Post)
		l.analyzeStmt(stmt.Body)
	case *ast.RangeStmt:
		l.enterAccess(writeAccessKind)
		l.analyzeExpr(stmt.Key)
		l.analyzeExpr(stmt.Value)
		l.leaveAccess()
		l.enterAccess(readAccessKind)
		l.analyzeExpr(stmt.X)
		l.leaveAccess()
		l.analyzeStmt(stmt.Body)
	case *ast.BadStmt, *ast.EmptyStmt, *ast.BranchStmt:
		// Skip
	}
}

func (l *lockAnalyzer) analyzeExprs(exprs []ast.Expr) {
	for _, expr := range exprs {
		l.analyzeExpr(expr)
	}
}

func (l *lockAnalyzer) enter(nd ast.Node) {
	l.stack = append(l.stack, nd)
}

func (l *lockAnalyzer) leave() {
	ln := len(l.stack)
	if ln > 0 {
		l.stack[ln-1] = nil
		l.stack = l.stack[0 : ln-1]
	}
}

func (l *lockAnalyzer) isProtectedBy(prots []protection, expr ast.Expr, access accessKind) bool {
	return l.lockScopes[len(l.lockScopes)-1].isProtectedByAll(prots, expr, access, l.pass)
}

// Check if we're within the execution path of a defer statement. The way we find if we're called within a
// defer call is generalized as follows: keep moving upwards the tree, and if we find a defer
// statement before we find an object that invalidates the defer scope (ast.FuncDecl or ast.FuncLit),
// then we are within a deferred call.
func (l *lockAnalyzer) isWithinDeferScope() bool {
	for i := len(l.stack) - 2; i >= 0; i-- {
		switch l.stack[i].(type) {
		case *ast.DeferStmt:
			return true
		case *ast.FuncLit, *ast.FuncDecl:
			return false
		}
	}
	return false
}

func (l *lockAnalyzer) analyzeExpr(expr ast.Expr) {
	if expr == nil {
		return
	}

	l.enter(expr)
	defer l.leave()

	switch expr := expr.(type) {
	case *ast.Ident:
		switch obj := l.pass.TypesInfo.ObjectOf(expr).(type) {
		case *types.Var:
			if parent, ok := ancestorAs[*ast.SelectorExpr](l, 1); ok {
				protStr := ""
				many := false
				for _, prot := range l.protections.getAll(obj) {
					if many {
						protStr += ", "
					}
					protStr += prot.lockObj.Name()
					many = true
				}
				if !l.isProtectedBy(l.protections.getAll(obj), parent.X, l.currentAccess()) {
					l.pass.Reportf(expr.Pos(), "%s is not held while accessing %s", protStr, obj.Name())
				}
			}
		case *types.Func:
			// TODO handle TryLock
			// TODO consider the case when a protected function is used as a variable (passed/returned)?

			if parent, ok := ancestorAs[*ast.SelectorExpr](l, 1); ok {
				// Make sure this is a call expr, because it may just be a function selector (e.g. s.mut.Lock).
				if _, ok := ancestorAs[*ast.CallExpr](l, 2); ok {
					if !l.isProtectedBy(l.protections.getAll(obj), parent.X, l.currentAccess()) {
						protStr := ""
						many := false
						for _, prot := range l.protections.getAll(obj) {
							if many {
								protStr += ", "
							}
							protStr += prot.lockObj.Name()
							many = true
						}
						l.pass.Reportf(expr.Pos(), "%s is not held while accessing %s", protStr, obj.Name())
					}

					if typ := l.pass.TypesInfo.TypeOf(parent.X); typ != nil {
						_, isCall := parent.X.(*ast.CallExpr)
						l.inspectLockCall(parent.X, obj.Name(), lockKindOf(typ, !isCall))
					}
				}
			}
		}
	case *ast.Ellipsis:
		l.analyzeExpr(expr.Elt)
	case *ast.FuncLit:
		// Function literals generally require a new lock scope as we don't know where the function will be executed
		// (e.g. a callback passed to another thread). However, we retain the current lock scope if this literal is
		// part of a call expression. In that case, know that the function will be executed inline. This heuristic allows
		// expressions like func() { ... }() to not lose lock holding status.
		// TODO we can add other heuristics that check if the function is passed as a lambda to another std function that
		//     is known to execute things inline.
		// TODO If we feel adventurous, we can also track function literal assignments (e.g. fn = func() { ... }) and only
		//     warn about the lock analysis results if the variable goes out of scope (passed somewhere or returned).
		// TODO this will be a false positive inside go statements because the function will be executed on another thread.
		_, retainLockScope := ancestorAs[*ast.CallExpr](l, 1)
		if !retainLockScope {
			l.enterLockScope()
		}
		l.enterDeferScope()

		l.analyzeStmt(expr.Body)

		if !retainLockScope {
			l.exitLockScope()
		}
		l.exitDeferScope()
	case *ast.CompositeLit:
		for _, el := range expr.Elts {
			l.analyzeExpr(el)
		}
	case *ast.ParenExpr:
		l.analyzeExpr(expr.X)
	case *ast.SelectorExpr:
		l.analyzeExpr(expr.X)
		l.analyzeExpr(expr.Sel)
	case *ast.IndexExpr:
		l.analyzeExpr(expr.X)
		l.analyzeExpr(expr.Index)
	case *ast.IndexListExpr:
		l.analyzeExpr(expr.X)
		for _, ind := range expr.Indices {
			l.analyzeExpr(ind)
		}
	case *ast.SliceExpr:
		l.analyzeExpr(expr.X)
		l.analyzeExpr(expr.Low)
		l.analyzeExpr(expr.High)
		l.analyzeExpr(expr.Max)
	case *ast.TypeAssertExpr:
		l.analyzeExpr(expr.X)
	case *ast.CallExpr:
		l.analyzeExpr(expr.Fun)
		for _, arg := range expr.Args {
			l.analyzeExpr(arg)
		}
	case *ast.StarExpr:
		l.analyzeExpr(expr.X)
	case *ast.UnaryExpr:
		l.analyzeExpr(expr.X)
	case *ast.BinaryExpr:
		l.analyzeExpr(expr.X)
		l.analyzeExpr(expr.Y)
	case *ast.KeyValueExpr:
		// We're not interested in the key.
		l.analyzeExpr(expr.Value)
	case *ast.BasicLit, *ast.BadExpr:
		// Skip
	}
}

// Checks if this is a lock or unlock.
func (l *lockAnalyzer) inspectLockCall(selector ast.Expr, funcName string, kind lockKind) {
	// We'll need to inspect the variable or function expression on which this function is called.
	if lockIdent := findLockIdent(selector); lockIdent != nil {
		if lockObj := l.pass.TypesInfo.ObjectOf(lockIdent); lockObj != nil {
			if selectorParent, ok := parentOf(selector); ok {
				if root, ok := findRootObj(selectorParent, l.pass); ok {
					if kind.isLocking(funcName) {
						l.lock(lockObj, root, heldLock{
							isRead:   funcName[0] == 'R',
							kind:     kind,
							selector: selector,
						})
					} else if kind.isUnlocking(funcName) {
						if l.isWithinDeferScope() {
							l.deferredUnlock(lockObj, root, heldLock{
								kind:     kind,
								selector: selector,
							})
						} else {
							l.unlock(lockObj, root, heldLock{
								isRead:   funcName[0] == 'R',
								kind:     kind,
								selector: selector,
							})
						}
					}
				}
			}
		}
	}
}

// Check if the two expressions match. Matching is made nominally rather than canonically.
// For canonical matching, it is enough to check if the root selector of the given expressions
// match canonically (i.e. by pass.TypesInfo.ObjectOf(rootIdent). The canonical identity follows
// even if we match the rest of the expressions nominally.
func expressionsMatch(left ast.Expr, right ast.Expr) bool {
	if left == nil && right == nil {
		return true
	} else if left == nil || right == nil {
		return false
	}

	// Although this is not right from an evaluation perspective, it suffices for current usage, where expressions are
	// expected to be lock/field selectors. Parenthesis won't matter in such case.
	if _, ok := left.(*ast.ParenExpr); ok {
		return expressionsMatch(ast.Unparen(left), right)
	}
	if _, ok := right.(*ast.ParenExpr); ok {
		return expressionsMatch(left, ast.Unparen(right))
	}

	switch left := left.(type) {
	case *ast.BadExpr:
		return false
	case *ast.BasicLit:
		if right, ok := right.(*ast.BasicLit); ok {
			return left.Kind == right.Kind && left.Value == right.Value
		}
	case *ast.BinaryExpr:
		if right, ok := right.(*ast.BinaryExpr); ok {
			return left.Op == right.Op && expressionsMatch(left.X, right.X) && expressionsMatch(left.Y, right.Y)
		}
	case *ast.CallExpr:
		if right, ok := right.(*ast.CallExpr); ok {
			return expressionsMatch(left.Fun, right.Fun) && expressionListMatches(left.Args, right.Args)
		}
	case *ast.CompositeLit:
		if right, ok := right.(*ast.CompositeLit); ok {
			return expressionsMatch(left.Type, right.Type) && expressionListMatches(left.Elts, right.Elts) && left.Incomplete == right.Incomplete
		}
	case *ast.Ellipsis:
		if right, ok := right.(*ast.Ellipsis); ok {
			return expressionsMatch(left.Elt, right.Elt)
		}
	case *ast.Ident:
		if right, ok := right.(*ast.Ident); ok && left.Name == right.Name {
			return true
		}
		return false
	case *ast.IndexExpr:
		if right, ok := right.(*ast.IndexExpr); ok {
			return expressionsMatch(left.X, right.X) && expressionsMatch(left.Index, right.Index)
		}
	case *ast.IndexListExpr:
		if right, ok := right.(*ast.IndexListExpr); ok {
			return expressionsMatch(left.X, right.X) && expressionListMatches(left.Indices, right.Indices)
		}
	case *ast.KeyValueExpr:
		if right, ok := right.(*ast.KeyValueExpr); ok {
			return expressionsMatch(left.Key, right.Key) && expressionsMatch(left.Value, right.Value)
		}
	case *ast.SelectorExpr:
		if right, ok := right.(*ast.SelectorExpr); ok {
			return expressionsMatch(left.X, right.X) && expressionsMatch(left.Sel, right.Sel)
		}
	case *ast.SliceExpr:
		if right, ok := right.(*ast.SliceExpr); ok {
			return expressionsMatch(left.X, right.X) && expressionsMatch(left.Low, right.Low) && expressionsMatch(left.High, right.High) && expressionsMatch(left.Max, right.Max) && left.Slice3 == right.Slice3
		}
	case *ast.StarExpr:
		if right, ok := right.(*ast.StarExpr); ok {
			return expressionsMatch(left.X, right.X)
		}
	case *ast.UnaryExpr:
		if right, ok := right.(*ast.UnaryExpr); ok {
			return left.Op == right.Op && expressionsMatch(left.X, right.X)
		}
	case *ast.FuncLit:
		// Matching two function literals would complicate things considerably as we'd have to match statement by statement.
		// And doing so isn't needed anyways.
		return false
	case *ast.TypeAssertExpr, *ast.StructType, *ast.MapType, *ast.InterfaceType, *ast.FuncType, *ast.ChanType, *ast.ArrayType:
		// We're not interested in types so we'll pass.
		return false
	}
	return false
}

func expressionListMatches(lefts []ast.Expr, rights []ast.Expr) bool {
	if len(lefts) != len(rights) {
		return false
	}

	for i, left := range lefts {
		if !expressionsMatch(left, rights[i]) {
			return false
		}
	}
	return true
}

// TODO this only takes into account the identifier names and not their canonical objects. We can canonicalize the expression w.r.t the strut type scope.
func trimSuffix(expr, suffix ast.Expr) (ast.Expr, bool) {
	switch expr := expr.(type) {
	case *ast.Ident:
		if suffix, ok := suffix.(*ast.Ident); ok && expr.Name == suffix.Name {
			return nil, true
		}
	case *ast.SelectorExpr:
		switch suffix := suffix.(type) {
		case *ast.SelectorExpr:
			if expr.Sel.Name == suffix.Sel.Name {
				return trimSuffix(expr.X, suffix.X)
			}
		case *ast.Ident:
			if expr.Sel.Name == suffix.Name {
				return expr.X, true
			}
		}
	case *ast.CallExpr:
		if suffix, ok := suffix.(*ast.CallExpr); ok {
			return trimSuffix(expr.Fun, suffix.Fun)
		}
	case *ast.ParenExpr:
		return trimSuffix(expr.X, suffix)
	}
	return nil, false
}

func findLockIdent(lockSelector ast.Expr) *ast.Ident {
	switch lockSelector := lockSelector.(type) {
	case *ast.Ident:
		return lockSelector
	case *ast.SelectorExpr:
		return lockSelector.Sel
	case *ast.CallExpr:
		return findLockIdent(lockSelector.Fun)
	case *ast.ParenExpr:
		return findLockIdent(lockSelector.X)
	default:
		return nil
	}
}

func parentOf(expr ast.Expr) (ast.Expr, bool) {
	switch expr := expr.(type) {
	case *ast.Ident:
		return nil, true
	case *ast.SelectorExpr:
		return expr.X, true
	case *ast.CallExpr:
		return parentOf(expr.Fun)
	case *ast.ParenExpr:
		return parentOf(expr.X)
	default:
		return nil, false
	}
}
