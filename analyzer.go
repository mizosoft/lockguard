package lockgaurd

import (
	"go/ast"
	"go/token"
	"go/types"
	"log"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
	"golang.org/x/tools/go/cfg"
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
	blocks      []*cfg.Block
}

func (l *lockAnalyzer) currentBlock() *cfg.Block {
	if len(l.blocks) == 0 {
		return nil
	}
	return l.blocks[len(l.blocks)-1]
}

func (l *lockAnalyzer) currentNode() ast.Node {
	if len(l.stack) == 0 {
		return nil
	}
	return l.stack[len(l.stack)-1]
}

func (l *lockAnalyzer) enterBlock(block *cfg.Block) {
	l.blocks = append(l.blocks, block)
	log.Printf("Entering CFG block: <%v>\n", block)
	l.currentLockScope().print(block)
}

func (l *lockAnalyzer) exitBlock() {
	block := l.currentBlock()
	log.Printf("Exiting CFG block: <%v>\n", block)
	l.currentLockScope().print(block)
	l.blocks = l.blocks[:len(l.blocks)-1]
}

func (l *lockAnalyzer) enterLockScope() {
	l.lockScopes = append(l.lockScopes, newLockScope())
}

func (l *lockAnalyzer) exitLockScope() {
	ln := len(l.lockScopes)
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
		l.analyzeCfg(decl.Body, nil)
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

func (l *lockAnalyzer) analyzeCfg(block *ast.BlockStmt, entry *cfg.Block) {
	g := cfg.New(block, func(expr *ast.CallExpr) bool {
		return true
	})

	log.Println("Generated CFG block", "\n`", g.Format(l.pass.Fset), "`")

	if entry != nil {
		l.currentLockScope().merge(entry, g.Blocks[0])
	}

	// If this block belongs to a function, and the function has protections, we'll assume these protections are held while
	// analyzing it. This allows other functions/variables which have the same protections to be called within this function.
	if funcDecl, ok := l.currentNode().(*ast.FuncDecl); ok {
		if fnc, ok := l.pass.TypesInfo.ObjectOf(funcDecl.Name).(*types.Func); ok {
			for _, prot := range l.protections[fnc] {
				lockFunc, unlockFunc := prot.defaultLockUnlockFuncs()
				lockPath := prot.lockPath
				if recv := fnc.Signature().Recv(); recv != nil && prot.withReceiver {
					lockPath = append(canonicalPath{recv}, lockPath...)
				}

				l.currentLockScope().apply(g.Blocks[0], copyAppend(lockPath, locateFromObjByName(prot.lockObj(), lockFunc)...))
				l.currentLockScope().applyDeferred(g.Blocks[0], copyAppend(lockPath, locateFromObjByName(prot.lockObj(), unlockFunc)...))
			}
		}
	}

	used := make(map[int32]bool)

	for _, block := range g.Blocks {
		if !block.Live {
			continue
		}

		if _, ok := used[block.Index]; ok {
			continue
		}

		used[block.Index] = true
		q := []*cfg.Block{block}
		for len(q) > 0 {
			b := q[0]
			q = q[1:]

			l.enterBlock(b)
			for _, nd := range b.Nodes {
				switch nd := nd.(type) {
				case ast.Stmt:
					l.analyzeStmt(nd)
				case ast.Expr:
					l.analyzeExpr(nd)
				case *ast.ValueSpec:
					l.analyzeExprs(nd.Values)
				}
			}
			l.exitBlock()

			// TODO handle back edges in case of loops. We can make the algorithm proceed to visit it one more time. That
			//      way things like for { mut.Lock() } will be caught.
			for _, succ := range b.Succs {
				l.currentLockScope().merge(b, succ)
				if _, ok := used[succ.Index]; !ok { // First time to visit.
					q = append(q, succ)
					used[succ.Index] = true
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
		l.analyzeCfg(stmt, l.currentBlock())
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
			// This check makes expressions like defer func(){ s.mut.Unlock() }() work.
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
				for _, warning := range l.currentLockScope().checkProtections(l.currentBlock(), canonicalPath{obj}, prots, l.currentAccess()) {
					l.pass.Reportf(expr.Pos(), "%s", warning)
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

		if retainLockScope {
			l.analyzeCfg(expr.Body, l.currentBlock()) // Follow up with current flow.
		} else {
			l.analyzeCfg(expr.Body, nil)
		}

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

		loc := infoLocator(l.pass.TypesInfo)

		xPath := loc.canonicalize(expr.X)
		if xPath == nil {
			log.Println("Unresolvable selector", types.ExprString(expr.X))
			return
		}

		switch obj := l.pass.TypesInfo.ObjectOf(expr.Sel).(type) {
		case *types.Var:
			fieldPath := locateFromObjByName(xPath[len(xPath)-1], obj.Name())
			if fieldPath == nil {
				log.Println("Unresolvable selector1", types.ExprString(expr.Sel), types.ExprString(expr))
				return
			}

			path := xPath
			for _, comp := range fieldPath {
				path = append(path, comp)
				if prots, ok := l.protections[comp]; ok {
					for _, warning := range l.currentLockScope().checkProtections(l.currentBlock(), path, prots, l.currentAccess()) {
						l.pass.Reportf(expr.Pos(), "%s", warning)
					}
				}
			}
		case *types.Func:
			funcPath := locateFromObjByName(xPath[len(xPath)-1], obj.Name())
			if funcPath == nil {
				log.Println("Unresolvable selector:", types.ExprString(expr.Sel), types.ExprString(expr))
				return
			}

			path := xPath
			for _, comp := range funcPath {
				path = append(path, comp)
				if prots, ok := l.protections[comp]; ok {
					for _, warning := range l.currentLockScope().checkProtections(l.currentBlock(), path, prots, l.currentAccess()) {
						l.pass.Reportf(expr.Pos(), "%s", warning)
					}
				}
			}

			// Check if this is a lock or unlock call.
			if call, isCall := ancestorAs[*ast.CallExpr](l, 1); isCall && isLockOpPath(path) {
				scope := l.currentLockScope()
				if l.isWithinDeferScope() {
					scope.applyDeferred(l.currentBlock(), path)
				} else {
					for _, warning := range scope.apply(l.currentBlock(), path) {
						l.pass.Reportf(call.Pos(), "%s", warning)
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
