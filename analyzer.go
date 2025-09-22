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

// Analyzer Checks lock-protected accesses.
var Analyzer = &analysis.Analyzer{
	Name:      "lockguard",
	Doc:       "Checks lock-protected accesses",
	Run:       run,
	Requires:  []*analysis.Analyzer{inspect.Analyzer},
	FactTypes: []analysis.Fact{new(protectedBy)},
}

type protectedBy struct {
	lock *types.Var
}

func (p *protectedBy) AFact() {}

func (p *protectedBy) String() string {
	return fmt.Sprintf("protected_by:\"%s\"", p.lock.Name())
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

	//ins.Preorder([]ast.Node{(*ast.FuncDecl)(nil)}, func(node ast.Node) {
	//	funcDecl := node.(*ast.FuncDecl)
	//	for _, comment := range funcDecl.Doc.List {
	//
	//	}
	//})

	f := &protectionsFinder{protections: make(map[types.Object]*types.Var)}
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
	typedParent, ok := l.stack[ln-upDepth-1].(N)
	return typedParent, ok
}

type lockAnalyzer struct {
	protections map[types.Object]*types.Var
	lockScopes  []map[*types.Var][]*ast.SelectorExpr
	deferScopes []map[*types.Var][]*ast.SelectorExpr
	pass        *analysis.Pass
	stack       []ast.Node
}

func (l *lockAnalyzer) enterLockScope() {
	l.lockScopes = append(l.deferScopes, make(map[*types.Var][]*ast.SelectorExpr))
}

func (l *lockAnalyzer) exitLockScope() {
	ln := len(l.lockScopes)
	l.lockScopes[ln-1] = nil
	l.lockScopes = l.lockScopes[0 : ln-1]
}

func (l *lockAnalyzer) enterDeferScope() {
	l.deferScopes = append(l.deferScopes, make(map[*types.Var][]*ast.SelectorExpr))
}

func (l *lockAnalyzer) exitDeferScope() {
	ln := len(l.deferScopes)
	scope := l.deferScopes[ln-1]
	l.deferScopes[ln-1] = nil
	l.deferScopes = l.deferScopes[0 : ln-1]
	for lockVar, selectors := range scope {
		l.unlock(lockVar, selectors)
	}
}

// TODO a wild idea: consider pointer assignment paths to check if we're referring to the same lock without
//      necessarily locking/unlocking it with the same expr.

func (l *lockAnalyzer) lock(lockVar *types.Var, lockSelector *ast.SelectorExpr) {
	fmt.Println("Locking", lockVar)
	ln := len(l.lockScopes)
	l.lockScopes[ln-1][lockVar] = append(l.lockScopes[ln-1][lockVar], lockSelector)
}

func (l *lockAnalyzer) unlock(lockVar *types.Var, lockSelectors []*ast.SelectorExpr) {
	fmt.Println("Unlocking", lockVar)
	edited := make([]*ast.SelectorExpr, 0)
	ln := len(l.lockScopes)
	for _, heldLockSelector := range l.lockScopes[ln-1][lockVar] {
		for _, lockSelector := range lockSelectors {
			if !expressionsMatch(l.pass, heldLockSelector.X, lockSelector.X) {
				edited = append(edited, heldLockSelector)
			}
		}
	}

	if len(edited) > 0 {
		l.lockScopes[ln-1][lockVar] = edited
	} else {
		delete(l.lockScopes[ln-1], lockVar)
	}
}

func (l *lockAnalyzer) deferredUnlock(lockVar *types.Var, lockSelector *ast.SelectorExpr) {
	fmt.Println("Deferred unlock of", lockVar)
	ln := len(l.deferScopes)
	l.deferScopes[ln-1][lockVar] = append(l.deferScopes[ln-1][lockVar], lockSelector)
}

func (l *lockAnalyzer) analyzeDecl(decl ast.Decl) {
	l.enterExpr(decl)
	defer l.leaveExpr()

	switch decl := decl.(type) {
	case *ast.FuncDecl:
		l.enterLockScope()
		l.enterDeferScope()
		l.analyzeStmt(decl.Body)
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

func (l *lockAnalyzer) isLockHeld(lockVar *types.Var, expr ast.Expr) bool {
	ln := len(l.lockScopes)
	for _, lockSelector := range l.lockScopes[ln-1][lockVar] {
		if expressionsMatch(l.pass, lockSelector.X, expr) {
			return true
		}
	}
	return false
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
			if lockVar, ok := l.protections[obj]; ok {
				if parent, ok := ancestorAs[*ast.SelectorExpr](l, 1); ok {
					if !l.isLockHeld(lockVar, parent.X) {
						l.pass.Reportf(expr.Pos(), "%s is not held while accessing %s", lockVar.Name(), obj.Name())
					}
				}
			}
		case *types.Func:
			// TODO handle function checks when we allow protecting them via comments.
			// TODO handle TryLock

			// Check if this is a lock or unlock. We'll need to inspect the variable on which this function is called.
			if obj.Name() == "Lock" || obj.Name() == "Unlock" {
				if parent, ok := ancestorAs[*ast.SelectorExpr](l, 1); ok {
					if _, ok := ancestorAs[*ast.CallExpr](l, 2); ok { // Make sure this is a call expr.
						if typ := l.pass.TypesInfo.TypeOf(parent.X); typ != nil && (types.Implements(typ, lockerType) || types.Implements(types.NewPointer(typ), lockerType)) {
							if lockSelector, ok := parent.X.(*ast.SelectorExpr); ok {
								if lockVar, ok := l.pass.TypesInfo.ObjectOf(lockSelector.Sel).(*types.Var); ok {
									if obj.Name() == "Lock" {
										l.lock(lockVar, lockSelector)
									} else if obj.Name() == "Unlock" {
										if l.isWithinDeferScope() {
											l.deferredUnlock(lockVar, lockSelector)
										} else {
											l.unlock(lockVar, []*ast.SelectorExpr{lockSelector})
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
		// TODO this will fail inside go statements because the function will be executed on another thread.
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

func expressionsMatch(pass *analysis.Pass, left ast.Expr, right ast.Expr) bool {
	if left == nil && right == nil {
		return true
	} else if left == nil || right == nil {
		return false
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
			return left.Op == right.Op && expressionsMatch(pass, left.X, right.X) && expressionsMatch(pass, left.Y, right.Y)
		}
	case *ast.CallExpr:
		if right, ok := right.(*ast.CallExpr); ok {
			return expressionsMatch(pass, left.Fun, right.Fun) && expressionListMatches(pass, left.Args, right.Args)
		}
	case *ast.CompositeLit:
		if right, ok := right.(*ast.CompositeLit); ok {
			return expressionsMatch(pass, left.Type, right.Type) && expressionListMatches(pass, left.Elts, right.Elts) && left.Incomplete == right.Incomplete
		}
	case *ast.Ellipsis:
		if right, ok := right.(*ast.Ellipsis); ok {
			return expressionsMatch(pass, left.Elt, right.Elt)
		}
	case *ast.Ident:
		if right, ok := right.(*ast.Ident); ok {
			if leftObj := pass.TypesInfo.ObjectOf(left); leftObj != nil {
				if rightObj := pass.TypesInfo.ObjectOf(right); rightObj != nil {
					return leftObj == rightObj
				}
			}
		}
		return false
	case *ast.IndexExpr:
		if right, ok := right.(*ast.IndexExpr); ok {
			return expressionsMatch(pass, left.X, right.X) && expressionsMatch(pass, left.Index, right.Index)
		}
	case *ast.IndexListExpr:
		if right, ok := right.(*ast.IndexListExpr); ok {
			return expressionsMatch(pass, left.X, right.X) && expressionListMatches(pass, left.Indices, right.Indices)
		}
	case *ast.KeyValueExpr:
		if right, ok := right.(*ast.KeyValueExpr); ok {
			return expressionsMatch(pass, left.Key, right.Key) && expressionsMatch(pass, left.Value, right.Value)
		}
	case *ast.ParenExpr:
		if right, ok := right.(*ast.ParenExpr); ok {
			return expressionsMatch(pass, left.X, right.X)
		}
	case *ast.SelectorExpr:
		if right, ok := right.(*ast.SelectorExpr); ok {
			return expressionsMatch(pass, left.X, right.X) && expressionsMatch(pass, left.Sel, right.Sel)
		}
	case *ast.SliceExpr:
		if right, ok := right.(*ast.SliceExpr); ok {
			return expressionsMatch(pass, left.X, right.X) && expressionsMatch(pass, left.Low, right.Low) && expressionsMatch(pass, left.High, right.High) && expressionsMatch(pass, left.Max, right.Max) && left.Slice3 == right.Slice3
		}
	case *ast.StarExpr:
		if right, ok := right.(*ast.StarExpr); ok {
			return expressionsMatch(pass, left.X, right.X)
		}
	case *ast.UnaryExpr:
		if right, ok := right.(*ast.UnaryExpr); ok {
			return left.Op == right.Op && expressionsMatch(pass, left.X, right.X)
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

func expressionListMatches(pass *analysis.Pass, lefts []ast.Expr, rights []ast.Expr) bool {
	if len(lefts) != len(rights) {
		return false
	}

	for i, left := range lefts {
		if !expressionsMatch(pass, left, rights[i]) {
			return false
		}
	}
	return true
}
