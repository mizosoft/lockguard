package lockgaurd

import (
	"fmt"
	"go/ast"
	"go/importer"
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
	FactTypes: []analysis.Fact{new(protectedBy)},
}

var lockerType *types.Interface

func init() {
	// Load sync package to get the Locker interface
	imp := importer.Default()
	syncPkg, err := imp.Import("sync")
	if err != nil {
		panic(err)
	}

	obj := syncPkg.Scope().Lookup("Locker")
	if typeName, ok := obj.(*types.TypeName); ok {
		if named, ok := typeName.Type().(*types.Named); ok {
			if iface, ok := named.Underlying().(*types.Interface); ok {
				lockerType = iface
			}
		}
	}
}

func run(pass *analysis.Pass) (interface{}, error) {
	if pass.Pkg.Name() != "a" {
		return nil, nil
	}

	if lockerType == nil {
		return nil, fmt.Errorf("unable to find sync.Locker interface type")
	}

	ins := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	f := &protectionsFinder{protections: make(map[types.Object]protection)}
	f.find(pass, ins)

	l := &lockAnalyzer{
		protections: f.protections,
		pass:        pass,
		stack:       make([]ast.Node, 0),
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

func closest[N ast.Node](l *lockAnalyzer) (N, bool) {
	ln := len(l.stack)
	for i := ln - 2; i >= 0; i-- {
		if typedNode, ok := l.stack[i].(N); ok {
			return typedNode, true
		}
	}
	return nillOf[N](), false
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

type lockScope struct {
	locks map[*types.Var]map[types.Object][]ast.Expr // lockVar -> root selector (nil if global) -> list of expressions selecting the lock.
}

func (s *lockScope) add(lockVar *types.Var, root types.Object, lockSelector ast.Expr) {
	locksForVar, ok := s.locks[lockVar]
	if !ok {
		locksForVar = make(map[types.Object][]ast.Expr)
		s.locks[lockVar] = locksForVar
	}
	locksForVar[root] = append(locksForVar[root], lockSelector)
}

func (s *lockScope) remove(lockVar *types.Var, root types.Object, lockSelector ast.Expr) {
	edited := make([]ast.Expr, 0)
	for _, existingSelector := range s.locks[lockVar][root] {
		if !expressionsMatch(existingSelector, lockSelector) {
			edited = append(edited, existingSelector)
		}
	}

	if len(edited) > 0 {
		s.locks[lockVar][root] = edited
	} else {
		delete(s.locks[lockVar], root)
		if len(s.locks[lockVar]) == 0 {
			delete(s.locks, lockVar)
		}
	}
}

func (s *lockScope) removeAll(lockVar *types.Var, root types.Object, lockSelectors []ast.Expr) {
	edited := make([]ast.Expr, 0)
	for _, existingSelector := range s.locks[lockVar][root] {
		add := true
		for _, lockSelector := range lockSelectors {
			if expressionsMatch(existingSelector, lockSelector) {
				add = false
				break
			}
		}

		if add {
			edited = append(edited, existingSelector)
		}
	}

	if len(edited) > 0 {
		s.locks[lockVar][root] = edited
	} else {
		delete(s.locks[lockVar], root)
		if len(s.locks[lockVar]) == 0 {
			delete(s.locks, lockVar)
		}
	}
}

func (s *lockScope) isLockHeldBy(expr ast.Expr, prot protection, pass *analysis.Pass) bool {
	root, ok := findRootObj(expr, pass)
	if !ok {
		return false
	}

	for _, lockSelector := range s.locks[prot.lockVar][root] {
		if trimmedLockSelector, ok := trimSuffix(lockSelector, prot.lockExpr); ok && expressionsMatch(trimmedLockSelector, expr) {
			return true
		}
	}
	return false
}

type lockAnalyzer struct {
	protections map[types.Object]protection
	lockScopes  []*lockScope
	deferScopes []*lockScope
	pass        *analysis.Pass
	stack       []ast.Node
}

func (l *lockAnalyzer) enterLockScope() {
	l.lockScopes = append(l.lockScopes, &lockScope{
		locks: make(map[*types.Var]map[types.Object][]ast.Expr),
	})
}

func (l *lockAnalyzer) exitLockScope() {
	ln := len(l.lockScopes)
	l.lockScopes[ln-1] = nil
	l.lockScopes = l.lockScopes[0 : ln-1]
}

func (l *lockAnalyzer) enterDeferScope() {
	l.deferScopes = append(l.deferScopes, &lockScope{
		locks: make(map[*types.Var]map[types.Object][]ast.Expr),
	})
}

func (l *lockAnalyzer) exitDeferScope() {
	ln := len(l.deferScopes)
	scope := l.deferScopes[ln-1]
	l.deferScopes[ln-1] = nil
	l.deferScopes = l.deferScopes[0 : ln-1]
	for lockVar, roots := range scope.locks {
		for root, exprs := range roots {
			l.unlockAll(lockVar, root, exprs)
		}
	}
}

// TODO a wild idea: consider pointer assignment paths to check if we're referring to the same lock without
//      necessarily locking/unlocking it with the same expr.

func (l *lockAnalyzer) lock(lockVar *types.Var, root types.Object, lockSelector ast.Expr) {
	fmt.Println("Locking", lockVar, "for root", root, "with lockSelector", types.ExprString(lockSelector))
	l.lockScopes[len(l.lockScopes)-1].add(lockVar, root, lockSelector)
}

func (l *lockAnalyzer) unlock(lockVar *types.Var, root types.Object, lockSelector ast.Expr) {
	fmt.Println("Unlocking", lockVar, "for root", root, "with lockSelector", types.ExprString(lockSelector))
	l.lockScopes[len(l.lockScopes)-1].remove(lockVar, root, lockSelector)
}

func (l *lockAnalyzer) unlockAll(lockVar *types.Var, root types.Object, lockSelectors []ast.Expr) {
	str := ""
	for _, s := range lockSelectors {
		str += types.ExprString(s)
	}
	fmt.Println("Unlocking", lockVar, "for root", root, "with lockSelectors", str)
	l.lockScopes[len(l.lockScopes)-1].removeAll(lockVar, root, lockSelectors)
}

func (l *lockAnalyzer) deferredUnlock(lockVar *types.Var, root types.Object, lockSelector ast.Expr) {
	ln := len(l.deferScopes)
	l.deferScopes[ln-1].add(lockVar, root, lockSelector)
}

func (l *lockAnalyzer) analyzeDecl(decl ast.Decl) {
	l.enterExpr(decl)
	defer l.leaveExpr()

	switch decl := decl.(type) {
	case *ast.FuncDecl:
		l.enterLockScope()
		l.enterDeferScope()

		// If this function is protected by a lock, we'll assume this lock is held while analyzing it. This allows other
		// functions/variables protected by the same lock to be called within this function.
		var explicitLock struct {
			prot protection
			recv *types.Var
		}
		if fnc, ok := l.pass.TypesInfo.ObjectOf(decl.Name).(*types.Func); ok {
			if prot, ok := l.protections[fnc]; ok {
				if recv := fnc.Signature().Recv(); recv != nil {
					explicitLock.prot, explicitLock.recv = prot, recv
					l.lock(prot.lockVar, recv, prot.lockExprWithReceiver)
				}
			}
		}

		l.analyzeStmt(decl.Body)

		if explicitLock.recv != nil {
			l.unlock(explicitLock.prot.lockVar, explicitLock.recv, explicitLock.prot.lockExprWithReceiver)
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
	l.enterExpr(stmt)
	defer l.leaveExpr()

	switch stmt := stmt.(type) {
	case *ast.DeclStmt:
		l.analyzeDecl(stmt.Decl)
	case *ast.LabeledStmt:
		l.analyzeStmt(stmt.Stmt)
	case *ast.ExprStmt:
		l.analyzeExpr(stmt.X)
	case *ast.SendStmt:
		l.analyzeExpr(stmt.Chan)
		l.analyzeExpr(stmt.Value)
	case *ast.IncDecStmt:
		l.analyzeExpr(stmt.X)
	case *ast.AssignStmt:
		l.analyzeExprs(stmt.Lhs)
		l.analyzeExprs(stmt.Rhs)
	case *ast.GoStmt:
		l.analyzeExpr(stmt.Call)
	case *ast.DeferStmt:
		l.analyzeExpr(stmt.Call)
	case *ast.ReturnStmt:
		l.analyzeExprs(stmt.Results)
	case *ast.BlockStmt:
		for _, stmt := range stmt.List {
			l.analyzeStmt(stmt)
		}
	case *ast.IfStmt:
		l.analyzeStmt(stmt.Init)
		l.analyzeExpr(stmt.Cond)
		l.analyzeStmt(stmt.Body)
		l.analyzeStmt(stmt.Else)
	case *ast.CaseClause:
		l.analyzeExprs(stmt.List)
		for _, innerStmt := range stmt.Body {
			l.analyzeStmt(innerStmt)
		}
	case *ast.SwitchStmt:
		l.analyzeStmt(stmt.Init)
		l.analyzeExpr(stmt.Tag)
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
		l.analyzeExpr(stmt.Cond)
		l.analyzeStmt(stmt.Post)
		l.analyzeStmt(stmt.Body)
	case *ast.RangeStmt:
		l.analyzeExpr(stmt.X)
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

func (l *lockAnalyzer) enterExpr(nd ast.Node) {
	l.stack = append(l.stack, nd)
}

func (l *lockAnalyzer) leaveExpr() {
	ln := len(l.stack)
	if ln > 0 {
		l.stack[ln-1] = nil
		l.stack = l.stack[0 : ln-1]
	}
}

func (l *lockAnalyzer) isLockHeldBy(expr ast.Expr, prot protection) bool {
	return l.lockScopes[len(l.lockScopes)-1].isLockHeldBy(expr, prot, l.pass)
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
	l.enterExpr(expr)
	defer l.leaveExpr()

	switch expr := expr.(type) {
	case *ast.Ident:
		switch obj := l.pass.TypesInfo.ObjectOf(expr).(type) {
		case *types.Var:
			if prot, ok := l.protections[obj]; ok {
				if parent, ok := ancestorAs[*ast.SelectorExpr](l, 1); ok {
					if !l.isLockHeldBy(parent.X, prot) {
						l.pass.Reportf(expr.Pos(), "%s is not held while accessing %s", prot.String(), obj.Name())
					}
				}
			}
		case *types.Func:
			// TODO handle TryLock
			// TODO consider the case when a protected function is used as a variable (passed/returned)?
			// TODO when analyzing a protected function, assume that the lock protecting it is held.

			if parent, ok := ancestorAs[*ast.SelectorExpr](l, 1); ok {
				// Make sure this is a call expr, because it may just be a function selector (e.g. s.mut.Lock).
				if _, ok := ancestorAs[*ast.CallExpr](l, 2); ok {
					if prot, ok := l.protections[obj]; ok {
						if !l.isLockHeldBy(parent.X, prot) {
							l.pass.Reportf(expr.Pos(), "%s is not held while accessing %s", prot.String(), obj.Name())
						}
					}

					// Check if this is a lock or unlock. We'll need to inspect the variable on which this function is called.
					if obj.Name() == "Lock" || obj.Name() == "Unlock" {
						if typ := l.pass.TypesInfo.TypeOf(parent.X); typ != nil && (types.Implements(typ, lockerType) || types.Implements(types.NewPointer(typ), lockerType)) {
							lockSelector := parent.X
							if lockIdent := findLockIdent(lockSelector); lockIdent != nil {
								if lockVar, ok := l.pass.TypesInfo.ObjectOf(lockIdent).(*types.Var); ok {
									if lockSelectorParent, ok := parentOf(lockSelector); ok {
										if root, ok := findRootObj(lockSelectorParent, l.pass); ok {
											if obj.Name() == "Lock" {
												l.lock(lockVar, root, lockSelector)
											} else if obj.Name() == "Unlock" {
												if l.isWithinDeferScope() {
													l.deferredUnlock(lockVar, root, lockSelector)
												} else {
													l.unlock(lockVar, root, lockSelector)
												}
											}
										}
									}
								}
							}
						}
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
		// And doing so doesn't seem to be needed anyways.
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
	case *ast.Ident:
		if suffix, ok := suffix.(*ast.Ident); ok && expr.Name == suffix.Name {
			return nil, true
		}
	case *ast.ParenExpr:
		return trimSuffix(expr.X, suffix)
	}
	return nil, false
}

func findLockIdent(lockSelector ast.Expr) *ast.Ident {
	switch lockSelector := lockSelector.(type) {
	case *ast.SelectorExpr:
		return lockSelector.Sel
	case *ast.Ident:
		return lockSelector
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
