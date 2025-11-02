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

	ins := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	f := newFinder(pass)
	f.find(ins)

	l := &lockAnalyzer{
		protections: f.protections,
		stack:       make([]ast.Node, 0),
		pass:        pass,
	}
	l.analyze(ins.Root())

	return nil, nil
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

func closest[N ast.Node](l *lockAnalyzer) (N, bool) {
	for i := len(l.stack) - 1; i >= 0; i-- {
		if typed, ok := l.stack[i].(N); ok {
			return typed, true
		}
	}
	return nillOf[N](), false
}

type accessKind int

const (
	readAccessKind accessKind = iota
	writeAccessKind
)

type lockAnalyzer struct {
	protections map[types.Object][]protection
	lockScopes  []*lockScope
	deferScopes []*lockScope
	stack       []ast.Node
	accessStack []accessKind
	pass        *analysis.Pass
	cursor      inspector.Cursor
	currentFile *ast.File
}

func (l *lockAnalyzer) enterLockScope() {
	l.lockScopes = append(l.lockScopes, newLockScope(l.pass))
}

func (l *lockAnalyzer) exitLockScope() {
	// TODO warn about unlocked locks.
	ln := len(l.lockScopes)
	l.lockScopes[ln-1].flushDeferred()
	l.lockScopes[ln-1] = nil
	l.lockScopes = l.lockScopes[0 : ln-1]
}

func (l *lockAnalyzer) currentLockScope() *lockScope {
	return l.lockScopes[len(l.lockScopes)-1]
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

// TODO too much panics

func (l *lockAnalyzer) analyze(cursor inspector.Cursor) {
	for c := range cursor.Preorder((*ast.File)(nil)) {
		l.currentFile = c.Node().(*ast.File)
		for c := range c.Preorder((*ast.FuncDecl)(nil), (*ast.GenDecl)(nil), (*ast.BadDecl)(nil)) {
			l.analyzeDecl(c.Node().(ast.Decl))
		}
	}
}

func (l *lockAnalyzer) analyzeDecl(decl ast.Decl) {
	l.enter(decl)
	defer l.exit()

	switch decl := decl.(type) {
	case *ast.FuncDecl:
		l.enterLockScope()

		// If this function is protected by a lock, we'll assume this lock is held while analyzing it. This allows other
		// functions/variables protected by the same lock to be called within this function.
		if fnc, ok := l.pass.TypesInfo.ObjectOf(decl.Name).(*types.Func); ok {
			for _, prot := range l.protections[fnc] {
				lockFunc, unlockFunc := prot.defaultLockUnlockFuncs()
				lockPath := prot.lockPath
				if recv := fnc.Signature().Recv(); recv != nil && prot.withReceiver {
					lockPath = append(canonicalPath{recv}, lockPath...)
				}

				l.currentLockScope().apply(lockPath, lockOpOf(lockFunc))
				l.currentLockScope().applyDeferred(lockPath, lockOpOf(unlockFunc))
			}
		}

		l.analyzeStmt(decl.Body)
		l.exitLockScope()
	case *ast.GenDecl:
		if decl.Tok == token.VAR {
			for _, spec := range decl.Specs {
				if valueSpec, isValueSpec := spec.(*ast.ValueSpec); isValueSpec {
					l.analyzeExprs(valueSpec.Values)
				}
			}
		}
	}
}

func (l *lockAnalyzer) analyzeStmt(stmt ast.Stmt) {
	if stmt == nil {
		return
	}

	l.enter(stmt)
	defer l.exit()

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
		l.enterAccess(readAccessKind) // We're reading the function to launch a goroutine with.
		l.enterLockScope()            // The function will be called in another thread, which requires a new lock scope.
		l.analyzeExpr(stmt.Call)
		l.exitLockScope()
		l.leaveAccess()
	case *ast.DeferStmt:
		l.enterAccess(readAccessKind) // We're reading the function to defer.
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
	case *ast.EmptyStmt, *ast.BranchStmt:
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

func (l *lockAnalyzer) exit() {
	ln := len(l.stack)
	if ln > 0 {
		l.stack[ln-1] = nil
		l.stack = l.stack[0 : ln-1]
	}
}

// Check if we're within the execution path of a defer statement. The way we find if we're called within a
// defer call is generalized as follows: keep moving upwards the tree, and if we find a defer statement before
// we find an object that invalidates the defer scope (ast.FuncDecl or ast.FuncLit that is not within a ast.CallExpr),
// then we are within a deferred call.
func (l *lockAnalyzer) isWithinDeferScope() bool {
	for i := len(l.stack) - 2; i >= 0; i-- {
		switch l.stack[i].(type) {
		case *ast.DeferStmt:
			return true
		case *ast.FuncDecl:
			return false
		case *ast.FuncLit:
			// This check makes defer func(){ s.mut.Unlock() }() work.
			if _, ok := ancestorAs[*ast.CallExpr](l, len(l.stack)-i); ok {
				return true
			}
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
	defer l.exit()

	switch expr := expr.(type) {
	case *ast.Ident:
		if obj := l.pass.TypesInfo.ObjectOf(expr); obj != nil {
			if prots, ok := l.protections[obj]; ok {
				if missedProts := l.currentLockScope().missedProtections(canonicalPath{obj}, prots, l.currentAccess()); len(missedProts) > 0 {
					protStr := ""
					many := false
					for _, prot := range missedProts {
						if many {
							protStr += ", "
						}
						protStr += prot.lockObj.Name()
						many = true
					}
					l.pass.Reportf(expr.Pos(), "%s is not held while accessing %s", protStr, obj.Name())
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

		l.analyzeStmt(expr.Body)

		if !retainLockScope {
			l.exitLockScope()
		}
	case *ast.CompositeLit:
		for _, el := range expr.Elts {
			l.analyzeExpr(el)
		}
	case *ast.ParenExpr:
		l.analyzeExpr(expr.X)
	case *ast.SelectorExpr:
		l.analyzeExpr(expr.X)

		// Fallback to file imports as TypesInfo.ObjectOf doesn't locate imported PkgName objects.
		loc := infoLocator(l.pass.TypesInfo).fallback(importsLocator(l.currentFile, l.pass.TypesInfo))

		xPath := loc.canonicalize(expr.X)
		if xPath == nil {
			fmt.Println("Unresolvable selector", types.ExprString(expr.X))
			return
		}

		switch obj := l.pass.TypesInfo.ObjectOf(expr.Sel).(type) {
		case *types.Var:
			fieldPath := locateFromObjByName(xPath[len(xPath)-1], obj.Name(), false)
			if fieldPath == nil {
				fmt.Println("Unresolvable selector1", types.ExprString(expr.Sel), types.ExprString(expr))
				return
			}

			path := xPath
			for _, comp := range fieldPath {
				path = append(path, comp)
				if prots, ok := l.protections[comp]; ok {
					if missedProts := l.currentLockScope().missedProtections(path, prots, l.currentAccess()); len(missedProts) > 0 {
						protStr := ""
						many := false
						for _, prot := range missedProts {
							if many {
								protStr += ", "
							}
							protStr += prot.lockObj.Name()
							many = true
						}
						l.pass.Reportf(expr.Pos(), "%s is not held while accessing %s", protStr, comp.Name())
					}
				}
			}
		case *types.Func:
			funcPath := locateFromObjByName(xPath[len(xPath)-1], obj.Name(), true)
			if funcPath == nil {
				fmt.Println("Unresolvable selector:", types.ExprString(expr.Sel), types.ExprString(expr))
				return
			}

			path := xPath
			for _, comp := range funcPath {
				path = append(path, comp)
				if prots, ok := l.protections[comp]; ok {
					if missedProts := l.currentLockScope().missedProtections(path, prots, l.currentAccess()); len(missedProts) > 0 {
						protStr := ""
						many := false
						for _, prot := range missedProts {
							if many {
								protStr += ", "
							}
							protStr += prot.lockObj.Name()
							many = true
						}
						l.pass.Reportf(expr.Pos(), "%s is not held while accessing %s", protStr, comp.Name())
					}
				}
			}

			// Check if this is a lock or unlock call.
			if _, isCall := ancestorAs[*ast.CallExpr](l, 1); isCall {
				path := path[:len(path)-1]
				if op := lockOpOf(obj.Name()); op != noneLockOp && isLockPath(path, op) {
					scope := l.currentLockScope()
					if l.isWithinDeferScope() {
						scope.applyDeferred(path, op)
					} else {
						scope.apply(path, op)
					}
				}
			}
		}
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
		// l.analyzeExpr(expr.Key)
		l.analyzeExpr(expr.Value)
	case *ast.BasicLit, *ast.BadExpr:
		// Skip
	}
}
