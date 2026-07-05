package lockguard

import (
	"go/ast"
	"go/token"
	"go/types"
	"log"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
	"golang.org/x/tools/go/cfg"
)

// TODO handle struct literals.
// TODO we should handle facts exported from other packages.
// TODO we can also support once.Do patterns.

var verbose bool

// Analyzer Checks lock-protected accesses and correct lock usage.
var Analyzer = &analysis.Analyzer{
	Name:      "lockguard",
	Doc:       "Checks lock-protected accesses",
	Run:       run,
	Requires:  []*analysis.Analyzer{inspect.Analyzer},
	FactTypes: []analysis.Fact{new(protectionFact)},
}

func init() {
	Analyzer.Flags.BoolVar(&verbose, "verbose", false, "print internal CFG and lock-state debug output")
}

func run(pass *analysis.Pass) (interface{}, error) {
	ins := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	// Skip runtime and internal packages: they can have very complex control flow
	// (e.g. runtime.mallocgc) that causes exponential DFS path exploration, and
	// they never contain lockguard annotations.
	pkgPath := pass.Pkg.Path()
	if pkgPath == "runtime" || strings.HasPrefix(pkgPath, "runtime/") ||
		pkgPath == "internal" || strings.HasPrefix(pkgPath, "internal/") ||
		pkgPath == "unsafe" {
		return nil, nil
	}

	protections := newFinder(pass).find(ins)
	l := &lockAnalyzer{
		protections:   protections,
		nodeStack:     make([]ast.Node, 0),
		pass:          pass,
		cpCoder:       newCannicalPathCoder(),
		eventRecorder: newEventRecorder(),
	}
	l.analyze(ins.Root())

	for _, diag := range l.eventRecorder.gatherDiagnostics() {
		pass.Report(analysis.Diagnostic{
			Pos:      diag.pos,
			Message:  diag.message,
			Category: string(diag.category),
		})
	}

	return nil, nil
}

func ancestorAs[N ast.Node](l *lockAnalyzer, upDepth int) (N, bool) {
	ln := len(l.nodeStack)
	if ln-upDepth-1 < 0 {
		return nillOf[N](), false
	}
	parent := l.nodeStack[ln-upDepth-1]
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
	protections        map[types.Object][]protection
	lockScopes         []*lockScope
	nodeStack          []ast.Node
	accessStack        []accessKind
	pass               *analysis.Pass
	blocks             []*cfg.Block
	cpCoder            *canonicalPathCoder
	eventRecorder      *eventRecorder
	deferredCallsStack [][][]any
}

// ownedAll is the ownership predicate for a top-level function exit: every held lock is reported.
func ownedAll(types.Object) bool { return true }

// ownedBy returns the ownership predicate for an inline function literal exit: a root variable
// belongs to the literal iff it is declared lexically inside it. This is decided by scope identity
// — walking the object's lexical scope chain up to the literal's function scope — so receivers,
// parameters and locals of enclosing functions (declared outside) flow onward, while the literal's
// own parameters and locals are leak-checked at the literal and pruned at its exit.
func (l *lockAnalyzer) ownedBy(funcLit *ast.FuncLit) func(types.Object) bool {
	fScope := l.pass.TypesInfo.Scopes[funcLit.Type]
	return func(obj types.Object) bool {
		if obj == nil || fScope == nil {
			return false
		}
		for sc := obj.Parent(); sc != nil; sc = sc.Parent() {
			if sc == fScope {
				return true
			}
		}
		return false
	}
}

func (l *lockAnalyzer) enterBlock(block *cfg.Block) {
	l.blocks = append(l.blocks, block)
	if verbose {
		log.Printf("Entering CFG block: <%v>\n", block)
		l.currentLockScope().print()
	}
}

func (l *lockAnalyzer) exitBlock() {
	block := l.currentBlock()
	if verbose {
		log.Printf("Exiting CFG block: <%v>\n", block)
		l.currentLockScope().print()
	}
	l.blocks = l.blocks[:len(l.blocks)-1]
}

func (l *lockAnalyzer) currentBlock() *cfg.Block {
	if len(l.blocks) == 0 {
		return nil
	}
	return l.blocks[len(l.blocks)-1]
}

func (l *lockAnalyzer) enterNewLockScope() {
	l.lockScopes = append(l.lockScopes, newLockScope())
}

func (l *lockAnalyzer) enterLockScope(scope *lockScope) {
	l.lockScopes = append(l.lockScopes, scope)
}

func (l *lockAnalyzer) currentNode() ast.Node {
	if len(l.nodeStack) == 0 {
		return nil
	}
	return l.nodeStack[len(l.nodeStack)-1]
}

func (l *lockAnalyzer) exitLockScope() {
	ln := len(l.lockScopes)
	l.lockScopes[ln-1] = nil
	l.lockScopes = l.lockScopes[0 : ln-1]
}

func (l *lockAnalyzer) currentLockScope() *lockScope {
	return l.lockScopes[len(l.lockScopes)-1]
}

func (l *lockAnalyzer) enterDeferScope() {
	l.deferredCallsStack = append(l.deferredCallsStack, make([][]any, 0))
}

func (l *lockAnalyzer) enterDeferBranch() {
	ln := len(l.deferredCallsStack)
	l.deferredCallsStack[ln-1] = append(l.deferredCallsStack[ln-1], []any{})
}

func (l *lockAnalyzer) appendDeferredCall(call any) {
	scope := l.deferredCallsStack[len(l.deferredCallsStack)-1]
	scope[len(scope)-1] = append(scope[len(scope)-1], call)
}

// currentDeferred flattens the current defer scope's branches into a single LIFO-ordered slice:
// later branches (deeper CFG blocks) first, and within each branch later calls first. This is the
// order deferred calls execute at function exit.
func (l *lockAnalyzer) currentDeferred() []any {
	scope := l.deferredCallsStack[len(l.deferredCallsStack)-1]
	var flat []any
	for i := len(scope) - 1; i >= 0; i-- {
		branch := scope[i]
		for j := len(branch) - 1; j >= 0; j-- {
			flat = append(flat, branch[j])
		}
	}
	return flat
}

func (l *lockAnalyzer) exitDeferBranch() {
	scope := l.deferredCallsStack[len(l.deferredCallsStack)-1]
	scope[len(scope)-1] = nil
	l.deferredCallsStack[len(l.deferredCallsStack)-1] = scope[:len(scope)-1]
}

func (l *lockAnalyzer) exitDeferScope() {
	ln := len(l.deferredCallsStack)
	l.deferredCallsStack[ln-1] = nil
	l.deferredCallsStack = l.deferredCallsStack[0 : ln-1]
}

func (l *lockAnalyzer) enterAccess(access accessKind) {
	l.accessStack = append(l.accessStack, access)
}

func (l *lockAnalyzer) exitAccess() {
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
	for c := range cursor.Preorder((*ast.FuncDecl)(nil), (*ast.GenDecl)(nil)) {
		l.analyzeDecl(c.Node().(ast.Decl))
	}
}

func (l *lockAnalyzer) analyzeDecl(decl ast.Decl) {
	l.enterNode(decl)
	defer l.exitNode()

	switch decl := decl.(type) {
	case *ast.FuncDecl:
		if decl.Body == nil {
			return // External/declared-only function; no stmt.Body to analyze.
		}

		body := decl.Body
		l.enterNewLockScope()
		l.analyzeCfg(body, func() {
			l.processExitDeferred(body.Rbrace, ownedAll)
		})
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

// analyzeCfg runs an analysis on the given block's CFG.
func (l *lockAnalyzer) analyzeCfg(stmt *ast.BlockStmt, onExit func()) {
	g := cfg.New(stmt, func(expr *ast.CallExpr) bool {
		return true
	})

	if verbose {
		log.Println("Generated CFG block", "\n`", g.Format(l.pass.Fset), "`")
	}

	// Each CFG analysis (a function body or a function literal) is its own defer scope. The base
	// branch holds function-level deferred calls (e.g. annotation-injected unlocks); each visited
	// CFG block pushes its own branch on top so per-block defers can be popped on DFS backtrack.
	l.enterDeferScope()
	defer l.exitDeferScope()
	l.enterDeferBranch()
	defer l.exitDeferBranch()

	// If this block belongs to a function, and the function has protections, we'll assume these
	// protections are held while analyzing it. This allows other functions/variables which have
	// the same protections to be called within this function.
	if funcDecl, ok := l.currentNode().(*ast.FuncDecl); ok {
		if fnc, ok := l.pass.TypesInfo.ObjectOf(funcDecl.Name).(*types.Func); ok {
			for _, prot := range l.protections[fnc] {
				lockFunc, unlockFunc := prot.defaultLockUnlockFuncs()
				lockPath := prot.lockPath
				if recv := fnc.Signature().Recv(); recv != nil && prot.withReceiver {
					lockPath = append(canonicalPath{recv}, lockPath...)
				}

				// Apply lock at entry (discard diagnostics — this is an annotation assumption).
				l.currentLockScope().apply(copyAppend(lockPath, locateFromObjByName(prot.lockObj(), lockFunc)...), token.NoPos)
				l.appendDeferredCall(canonicalPath(copyAppend(lockPath, locateFromObjByName(prot.lockObj(), unlockFunc)...)))
			}

			// Grant a lock-wrapper allowance when this method is the Lock/Unlock (or RLock/RUnlock) of a
			// Locker type: such methods acquire or release a lock across the call boundary, which looks
			// like a leak or an invalid unlock when analyzed in isolation. See lockScope.leakAllowance.
			if recv := fnc.Signature().Recv(); recv != nil {
				kind := lockKindOfObject(recv)
				name := funcDecl.Name.Name
				scope := l.currentLockScope()
				if kind.isLocking(name) {
					scope.leakAllowance = 1
					scope.leakAllowanceRLock = name == "RLock" || name == "TryRLock"
				} else if kind.isUnlocking(name) {
					scope.invalidReleaseAllowance = 1
				}
			}
		}
	}

	var visit func(*cfg.Block, *lockScope)

	// processTail runs the block's exit handling and successor recursion. It reads
	// l.currentLockScope() as the lock state reaching the end of the block, so an inline-IIFE seam
	// can re-run it once per inner exit path with that path's post-state.
	processTail := func(block *cfg.Block) {
		if len(block.Succs) == 0 && onExit != nil {
			onExit()
		}

		for _, succ := range block.Succs {
			// Skip back edges (loop cycles) to prevent infinite recursion. The current block stack (l.blocks) represents the
			// active DFS visit stack.
			isBackEdge := false
			for _, ancestor := range l.blocks {
				if ancestor == succ {
					isBackEdge = true
					break
				}
			}
			if isBackEdge {
				continue
			}

			forkedScope := l.currentLockScope().fork()

			// Perform Try[R]Lock analysis.
			// Extract the branch condition (last expression node, if any).
			var branchCond ast.Expr
			if len(block.Nodes) > 0 {
				branchCond, _ = block.Nodes[len(block.Nodes)-1].(ast.Expr)
				if branchCond != nil {
					var tlCalls []tryLockCall
					switch succ.Kind {
					case cfg.KindIfThen:
						tlCalls = l.evaluateTryLock(branchCond, true)
					case cfg.KindIfDone:
						// When there is no else clause, KindIfDone is a direct successor of the condition block
						// and represents the "false" (not-taken) branch. This handles the early-return TryLock
						// guard pattern:
						//   if !mu.TryLock() { return }
						//   // here TryLock succeeded — lock is held
						tlCalls = l.evaluateTryLock(branchCond, false)
					case cfg.KindIfElse:
						tlCalls = l.evaluateTryLock(branchCond, false)
					default:
						// Ignore.
					}

					// Apply Try[R]Lock() call results to the forked scope for the successor. A Try-lock
					// is non-blocking: it never deadlocks, so no acquire recorded here is a deadlock.
					prune := false
					for _, call := range tlCalls {
						lockObjPath := call.path[:len(call.path)-1]
						switch call.state {
						case trueTryLockState:
							// The success branch runs only when the lock was free, so it acquires the lock
							// (never a deadlock). If the lock is already definitely held, TryLock would have
							// returned false and this branch cannot execute, prune it rather than reporting
							// a spurious deadlock.
							result := forkedScope.lock(lockObjPath, call.isRLock, branchCond.Pos())
							if result.deadlock && !result.uncertain {
								prune = true
							} else {
								l.eventRecorder.recordAcquire(branchCond.Pos(), lockObjPath, call.isRLock, false, false)
							}
						case falseTryLockState:
							// Lock not acquired; nothing to apply.
						case unknownTryLockState:
							forkedScope.lockUncertain(lockObjPath, call.isRLock, branchCond.Pos())
							// Always uncertain: the TryLock result is unknown, so the acquire may or may not
							// have happened. It still never deadlocks.
							l.eventRecorder.recordAcquire(branchCond.Pos(), lockObjPath, call.isRLock, true, false)
						}
					}

					// The success branch is infeasible (lock already held); don't explore it, it is deadcode.
					// Note this behavior derrives from the assumption that the lock implementation is correct.
					if prune {
						continue
					}
				}
			}

			visit(succ, forkedScope)
		}
	}

	// processFrom analyzes block.Nodes[idx:] then the block tail. When it hits a statement-level
	// inline IIFE (func(){ ... }()), it treats the literal's CFG as a compressed node and
	// "decompresses" it: each of the literal's exit paths resumes processFrom at the next node with
	// that path's post-state. This lets locks the literal takes on enclosing-scope variables flow
	// into the rest of the function, while locks on its own locals are leak-checked at the literal.
	var processFrom func(block *cfg.Block, idx int)
	processFrom = func(block *cfg.Block, idx int) {
		for i := idx; i < len(block.Nodes); i++ {
			nd := block.Nodes[i]
			if funcLit, call, ok := stmtLevelInlineIife(nd); ok {
				// Arguments are evaluated in the enclosing scope, before the call runs.
				for _, arg := range call.Args {
					l.analyzeExpr(arg)
				}
				l.decompressInlineIife(funcLit, func() {
					processFrom(block, i+1)
				})
				return // The literal's exit paths own the continuation from here.
			}

			switch nd := nd.(type) {
			case ast.Stmt:
				l.analyzeStmt(nd)
			case ast.Expr:
				l.analyzeExpr(nd)
			case *ast.ValueSpec:
				l.analyzeExprs(nd.Values)
			}
		}

		processTail(block)
	}

	visit = func(block *cfg.Block, scope *lockScope) {
		// Don't evaluate unreachable blocks at all. go/cfg marks dead code (e.g. statements after a
		// return) as not Live, and leaves it without an in-edge so the DFS never reaches it anyway.
		// A default-less select that matches no case is the exception: go/cfg models it with a
		// terminal "after case" block that is graph-reachable (Live) yet can never execute — the
		// select blocks until a case is ready. Evaluating it would, for example, report a spurious
		// leak for a lock that is released on every real path. Skip both.
		if !block.Live || (block.Kind == cfg.KindSelectAfterCase && len(block.Succs) == 0) {
			return
		}

		l.enterBlock(block)
		l.enterDeferBranch()
		l.enterLockScope(scope)
		defer l.exitBlock()
		defer l.exitDeferBranch()
		defer l.exitLockScope()

		processFrom(block, 0)
	}

	visit(g.Blocks[0], l.currentLockScope().fork())
}

// stmtLevelInlineIife reports whether nd is a statement-level immediately-invoked function literal
// (func(){ ... }() as its own expression statement), returning the literal and its call. Only this
// form gets state-continuation treatment; literals nested inside larger expressions fall back to
// isolated analysis in analyzeExpr.
func stmtLevelInlineIife(nd ast.Node) (*ast.FuncLit, *ast.CallExpr, bool) {
	exprStmt, ok := nd.(*ast.ExprStmt)
	if !ok {
		return nil, nil, false
	}
	call, ok := ast.Unparen(exprStmt.X).(*ast.CallExpr)
	if !ok {
		return nil, nil, false
	}
	funcLit, ok := ast.Unparen(call.Fun).(*ast.FuncLit)
	if !ok {
		return nil, nil, false
	}
	return funcLit, call, true
}

// decompressInlineIife analyzes a statement-level inline function literal as a compressed node in
// the enclosing CFG. It analyzes the literal's body in a fresh leak frame (inheriting the current
// held-lock state), and for each exit path of the literal it resumes the enclosing function via
// continuation with the post-state: the literal's own defers run and its own-frame leaks are
// reported at the literal, then its locals are pruned and the frame demoted to the enclosing one so
// only locks on enclosing-scope variables flow onward.
func (l *lockAnalyzer) decompressInlineIife(funcLit *ast.FuncLit, continuation func()) {
	owned := l.ownedBy(funcLit)

	var seams []*lockScope
	l.enterLockScope(l.currentLockScope().fork())
	l.enterNode(funcLit)
	l.analyzeCfg(funcLit.Body, func() {
		l.processExitDeferred(funcLit.Body.Rbrace, owned)
		seams = append(seams, l.currentLockScope().detachOwned(owned))
	})
	l.exitNode()
	l.exitLockScope()

	for _, seam := range seams {
		l.enterLockScope(seam)
		continuation()
		l.exitLockScope()
	}
}

func (l *lockAnalyzer) recordLockResult(pos token.Pos, lockObjPath canonicalPath, res lockResult) {
	switch res := res.(type) {
	case acquiredLockResult:
		l.eventRecorder.recordAcquire(pos, lockObjPath, res.isRLock(), res.isUncertain(), res.deadlock)
	case releasedLockResult:
		l.eventRecorder.recordRelease(pos, lockObjPath, res.isRLock(), res.isUncertain(), res.invalid)
	}
}

func (l *lockAnalyzer) evaluateTryLock(expr ast.Expr, evalTarget bool) []tryLockCall {
	l.enterNode(expr)
	defer l.exitNode()

	switch expr := expr.(type) {
	case *ast.CallExpr:
		return l.evaluateTryLock(expr.Fun, evalTarget)
	case *ast.SelectorExpr:
		if _, inCall := ancestorAs[*ast.CallExpr](l, 1); !inCall {
			return nil
		}

		if fn, ok := l.pass.TypesInfo.ObjectOf(expr.Sel).(*types.Func); ok && (fn.Name() == "TryLock" || fn.Name() == "TryRLock") {
			loc := infoLocator(l.pass.TypesInfo)

			xPath := loc.canonicalize(expr.X)
			if xPath == nil {
				if verbose {
					log.Println("Unresolvable selector", types.ExprString(expr.X))
				}
				return nil
			}

			// Canonicalize the locking function path.
			var path canonicalPath
			if fn.Name() == "TryRLock" {
				path = append(xPath, locateFromObjByName(xPath[len(xPath)-1], "RLock")...)
			} else if fn.Name() == "TryLock" {
				path = append(xPath, locateFromObjByName(xPath[len(xPath)-1], "Lock")...)
			}

			if !isLockOpPath(path) {
				return nil // Not a lock.
			}

			var state tryLockState
			if evalTarget {
				state = trueTryLockState
			} else {
				state = falseTryLockState
			}

			var isRLock bool
			if fn.Name() == "TryRLock" {
				isRLock = true
			} else {
				isRLock = false
			}
			return []tryLockCall{
				{
					path:    path,
					state:   state,
					isRLock: isRLock,
				},
			}
		}
	case *ast.UnaryExpr:
		if expr.Op == token.NOT {
			return l.evaluateTryLock(expr.X, !evalTarget)
		}
	case *ast.BinaryExpr:
		if expr.Op == token.LAND { // &&
			if evalTarget {
				return mergeAnd(l.evaluateTryLock(expr.X, true), l.evaluateTryLock(expr.Y, true))
			} else {
				return mergeOr(l.evaluateTryLock(expr.X, false), l.evaluateTryLock(expr.Y, false))
			}
		} else if expr.Op == token.LOR { // ||
			if evalTarget {
				return mergeOr(l.evaluateTryLock(expr.X, true), l.evaluateTryLock(expr.Y, true))
			} else {
				return mergeAnd(l.evaluateTryLock(expr.X, false), l.evaluateTryLock(expr.Y, false))
			}
		}
	}
	return nil
}

func (l *lockAnalyzer) analyzeStmt(stmt ast.Stmt) {
	if stmt == nil {
		return
	}

	l.enterNode(stmt)
	defer l.exitNode()

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
		l.exitAccess()

		l.enterAccess(readAccessKind)
		l.analyzeExpr(stmt.Value)
		l.exitAccess()
	case *ast.IncDecStmt:
		l.enterAccess(writeAccessKind)
		l.analyzeExpr(stmt.X)
		l.exitAccess()
	case *ast.AssignStmt:
		l.enterAccess(writeAccessKind)
		l.analyzeExprs(stmt.Lhs)
		l.exitAccess()

		l.enterAccess(readAccessKind)
		l.analyzeExprs(stmt.Rhs)
		l.exitAccess()
	case *ast.GoStmt:
		l.enterAccess(readAccessKind) // We're reading the function to launch a goroutine with.
		l.enterNewLockScope()         // The function will be called in another thread, which requires a new lock scope.
		l.analyzeExpr(stmt.Call)
		l.exitLockScope()
		l.exitAccess()
	case *ast.DeferStmt:
		// Accumulate deferred calls instead of analyzing right away. They are replayed at each exit block in a function
		// CFG, simulating actual execution.
		l.appendDeferredCall(stmt.Call)
	case *ast.ReturnStmt:
		l.enterAccess(readAccessKind)
		l.analyzeExprs(stmt.Results)
		l.exitAccess()
	case *ast.BlockStmt:
		if b := l.currentBlock(); b == nil {
			l.analyzeCfg(stmt, nil)
		} else {
			log.Panicf("Unexpected block statement with CFG block: %v", b)
		}
	case *ast.IfStmt:
		l.analyzeStmt(stmt.Init)
		l.enterAccess(readAccessKind)
		l.analyzeExpr(stmt.Cond)
		l.exitAccess()
		l.analyzeStmt(stmt.Body)
		l.analyzeStmt(stmt.Else)
	case *ast.CaseClause:
		l.enterAccess(readAccessKind)
		l.analyzeExprs(stmt.List)
		l.exitAccess()
		for _, innerStmt := range stmt.Body {
			l.analyzeStmt(innerStmt)
		}
	case *ast.SwitchStmt:
		l.analyzeStmt(stmt.Init)
		l.enterAccess(readAccessKind)
		l.analyzeExpr(stmt.Tag)
		l.exitAccess()
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
		l.exitAccess()
		l.analyzeStmt(stmt.Post)
		l.analyzeStmt(stmt.Body)
	case *ast.RangeStmt:
		l.enterAccess(writeAccessKind)
		l.analyzeExpr(stmt.Key)
		l.analyzeExpr(stmt.Value)
		l.exitAccess()
		l.enterAccess(readAccessKind)
		l.analyzeExpr(stmt.X)
		l.exitAccess()
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

func (l *lockAnalyzer) enterNode(nd ast.Node) {
	l.nodeStack = append(l.nodeStack, nd)
}

func (l *lockAnalyzer) exitNode() {
	ln := len(l.nodeStack)
	if ln > 0 {
		l.nodeStack[ln-1] = nil
		l.nodeStack = l.nodeStack[0 : ln-1]
	}
}

func (l *lockAnalyzer) checkProtections(pos token.Pos, path canonicalPath, prots []protection, access accessKind) {
	l.eventRecorder.recordAccess(pos, path, access, l.currentLockScope().checkProtections(path, access, prots))
}

func (l *lockAnalyzer) analyzeExpr(expr ast.Expr) {
	if expr == nil {
		return
	}

	l.enterNode(expr)
	defer l.exitNode()

	switch expr := expr.(type) {
	case *ast.Ident:
		if obj := l.pass.TypesInfo.ObjectOf(expr); obj != nil {
			if prots, ok := l.protections[obj]; ok {
				l.checkProtections(expr.Pos(), canonicalPath{obj}, prots, l.currentAccess())
			}
		}
	case *ast.Ellipsis:
		l.analyzeExpr(expr.Elt)
	case *ast.FuncLit:
		// Function literals generally require a new lock scope as we don't know where the function
		// will be executed (e.g. a callback passed to another thread). However, we retain the
		// current lock state if this literal is part of an inline call expression. In that case, we
		// know the function executes inline, so it sees the locks held at the call site. This lets
		// expressions like func() { ... }() keep lock-holding status for protection checks.
		//
		// This handles literals nested inside larger expressions (e.g. x := f(func(){...}())) and
		// callbacks/goroutines. Statement-level inline IIFEs are intercepted earlier in analyzeCfg and
		// decompressed so their lock effects flow into the enclosing function; here the literal is
		// analyzed in isolation (no state flow), with leaks scoped to its own declarations.
		// TODO we can add other heuristics that check if the function is passed as a lambda to
		//     another std function that is known to execute things inline.
		// TODO If we feel adventurous, we can also track function literal assignments
		//     (e.g. fn = func() { ... }) and only warn about the lock of analysis results if the
		//     variable goes out of scope (passed somewhere or returned).
		_, isInlineCall := ancestorAs[*ast.CallExpr](l, 1)
		var owned func(types.Object) bool
		if isInlineCall && len(l.lockScopes) > 0 {
			// Inline call: inherit the enclosing held-lock state, but only report leaks on the literal's
			// own variables (inherited enclosing locks belong to the caller, not this literal).
			l.enterLockScope(l.currentLockScope().fork())
			owned = l.ownedBy(expr)
		} else {
			// Callback / goroutine literal: executes in an unknown context, so start from an empty scope.
			// Every lock it holds at exit is its own leak.
			l.enterNewLockScope()
			owned = ownedAll
		}
		l.analyzeCfg(expr.Body, func() {
			l.processExitDeferred(expr.Body.Rbrace, owned)
		})
		l.exitLockScope()
	case *ast.CompositeLit:
		for _, el := range expr.Elts {
			l.analyzeExpr(el)
		}
	case *ast.ParenExpr:
		l.analyzeExpr(expr.X)
	case *ast.SelectorExpr:
		l.analyzeExpr(expr.X)

		xPath := infoLocator(l.pass.TypesInfo).canonicalize(expr.X)
		if xPath == nil {
			if verbose {
				log.Println("Unresolvable selector", types.ExprString(expr.X))
			}
			return
		}

		switch obj := l.pass.TypesInfo.ObjectOf(expr.Sel).(type) {
		case *types.Var:
			fieldPath := locateFromObjByName(xPath[len(xPath)-1], obj.Name())
			if fieldPath == nil {
				if verbose {
					log.Println("Unresolvable selector1", types.ExprString(expr.Sel), types.ExprString(expr))
				}
				return
			}

			path := xPath
			for _, comp := range fieldPath {
				path = append(path, comp)
				if prots, ok := l.protections[comp]; ok {
					l.checkProtections(expr.Sel.Pos(), path, prots, l.currentAccess())
				}
			}
		case *types.Func:
			funcPath := locateFromObjByName(xPath[len(xPath)-1], obj.Name())
			if funcPath == nil {
				if verbose {
					log.Println("Unresolvable selector:", types.ExprString(expr.Sel), types.ExprString(expr))
				}
				return
			}

			path := xPath
			for _, comp := range funcPath {
				path = append(path, comp)
				if prots, ok := l.protections[comp]; ok {
					l.checkProtections(expr.Sel.Pos(), path, prots, l.currentAccess())
				}
			}

			// Check if this is a lock or unlock call.
			if _, isCall := ancestorAs[*ast.CallExpr](l, 1); isCall && isLockOpPath(path) {
				l.recordLockResult(expr.Sel.Pos(), path[:len(path)-1], l.currentLockScope().apply(path, expr.Sel.Pos()))
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
		l.analyzeExpr(expr.Value)
	case *ast.BasicLit, *ast.BadExpr:
		// Skip
	}
}

// processExitDeferred replays the current defer scope's deferred calls at a function exit and
// records leak diagnostics. It is the entry point for an exit; replayDeferred does the work. owned
// selects which held roots are this function's leaks (see closePath).
func (l *lockAnalyzer) processExitDeferred(exitPos token.Pos, owned func(types.Object) bool) {
	l.replayDeferred(exitPos, owned, l.currentDeferred())
}

// replayDeferred applies the given deferred calls (already in LIFO execution order) to the current
// lock scope, then records the leak-collection point. calls holds either canonicalPath
// (annotation-injected unlock) or *ast.CallExpr (user defer).
//
// When a func-literal deferred call is encountered, the DFS continues into its body rather than
// stopping: each inner exit path becomes its own leak-collection point that first runs the func
// literal's own defers, then the remaining outer calls. This naturally captures uncertainty: if
// only some inner paths release a lock, the exitPaths/leakEvents counts diverge and the gather
// pass emits "possibly held".
//
// exitPos is the closing brace of the function whose exit we're processing, used for all leak
// events on this path. owned selects which held roots are this function's leaks (see closePath); it
// threads unchanged into deferred func literals, which run as part of this same function's exit.
func (l *lockAnalyzer) replayDeferred(exitPos token.Pos, owned func(types.Object) bool, calls []any) {
	for i := 0; i < len(calls); i++ {
		switch c := calls[i].(type) {
		case canonicalPath:
			l.currentLockScope().apply(c, token.NoPos)
		case *ast.CallExpr:
			if funcLit, ok := c.Fun.(*ast.FuncLit); ok {
				// remaining: outer calls with lower LIFO priority that must still run after this func
				// literal finishes (and after the func literal's own defers).
				remaining := append([]any(nil), calls[i+1:]...)

				// A deferred func literal runs as part of the enclosing function's exit, so locks it
				// acquires belong to the enclosing function: fork (inherit state) and keep the same owned
				// predicate.
				l.enterLockScope(l.currentLockScope().fork())
				l.enterNode(funcLit)
				l.analyzeCfg(funcLit.Body, func() {
					// At each of the func literal's exit paths, run its own defers, then the remaining
					// outer defers, and only then collect leaks (handled by the recursion's tail).
					l.replayDeferred(exitPos, owned, append(l.currentDeferred(), remaining...))
				})
				l.exitNode()
				l.exitLockScope()
				return // The inner DFS handles leak collection for this path.
			}
			l.analyzeExpr(c)
		}
	}

	// All deferred calls applied; collect leaks on this path.
	l.eventRecorder.recordExitPath(exitPos)
	for _, leak := range l.currentLockScope().closePath(owned) {
		l.eventRecorder.recordLeak(exitPos, leak.path, leak.uncertain, leak.rlock, leak.acquirePos)
	}
}
