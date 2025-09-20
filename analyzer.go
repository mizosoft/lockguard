package main

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"reflect"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

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

func run(pass *analysis.Pass) (interface{}, error) {
	if pass.Pkg.Name() != "a" {
		return nil, nil
	}

	// Load sync package to get the Locker interface
	imp := importer.Default()
	syncPkg, err := imp.Import("sync")
	if err != nil {
		return nil, err
	}

	obj := syncPkg.Scope().Lookup("Locker")
	if typeName, ok := obj.(*types.TypeName); ok {
		if named, ok := typeName.Type().(*types.Named); ok {
			if iface, ok := named.Underlying().(*types.Interface); ok {
				lockerType = iface
			}
		}
	}

	if lockerType == nil {
		return nil, fmt.Errorf("unable to find sync.Locker interface type")
	}

	ins := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	f := &protectionsFinder{protections: make(map[types.Object]*types.Var)}
	f.find(pass, ins)

	l := &lockAnalyzer{
		protections: f.protections,
		locks:       make(map[*types.Var][]*ast.SelectorExpr),
		pass:        pass,
		stack:       make([]ast.Node, 0),
	}
	ins.Preorder([]ast.Node{(*ast.FuncDecl)(nil), (*ast.GenDecl)(nil), (*ast.BadDecl)(nil)}, func(node ast.Node) {
		l.analyzeDecl(node.(ast.Decl))
	})

	return nil, nil
}

type lockAnalyzer struct {
	protections     map[types.Object]*types.Var
	locks           map[*types.Var][]*ast.SelectorExpr
	deferredUnlocks []map[*types.Var]bool
	pass            *analysis.Pass
	stack           []ast.Node
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

func parentAs[N ast.Node](l *lockAnalyzer) (N, bool) {
	ln := len(l.stack)
	if ln <= 1 {
		return nillOf[N](), false
	}
	typedParent, ok := l.stack[ln-2].(N)
	return typedParent, ok
}

func (l *lockAnalyzer) analyzeDecl(decl ast.Decl) {
	l.enter(decl)
	defer l.leave()

	switch decl := decl.(type) {
	case *ast.FuncDecl:
		l.deferredUnlocks = append(l.deferredUnlocks, make(map[*types.Var]bool))

		l.analyzeStmt(decl.Body)

		ln := len(l.deferredUnlocks)
		unlocked := l.deferredUnlocks[ln-1]
		l.deferredUnlocks[ln-1] = nil
		l.deferredUnlocks = l.deferredUnlocks[0 : ln-1]
		for lockVar := range unlocked {
			delete(l.locks, lockVar)
		}
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

func (l *lockAnalyzer) isLockHeld(lockVar *types.Var, expr ast.Expr) bool {
	for _, lockSelector := range l.locks[lockVar] {
		if expressionsMatch(l.pass, lockSelector.X, expr) {
			return true
		}
	}
	return false
}

func (l *lockAnalyzer) analyzeExpr(expr ast.Expr) {
	l.enter(expr)
	defer l.leave()

	switch expr := expr.(type) {
	case *ast.Ident:
		switch obj := l.pass.TypesInfo.ObjectOf(expr).(type) {
		case *types.Var:
			if lockVar, ok := l.protections[obj]; ok {
				if parent, ok := parentAs[*ast.SelectorExpr](l); ok {
					if l.isLockHeld(lockVar, parent.X) {
						fmt.Println("Lock", lockVar, "is held while accessing", obj)
					} else {
						fmt.Println("Lock", lockVar, "is not held while accessing", obj)
					}
				}
			}
		case *types.Func:
			// TODO handle function checks when we allow protecting them via comments.

			// Check if this is a lock or unlock. We'll need to inspect the variable on which this function is called.
			if obj.Name() == "Lock" || obj.Name() == "Unlock" {
				if parent, ok := parentAs[*ast.SelectorExpr](l); ok {
					if typ := l.pass.TypesInfo.TypeOf(parent.X); typ != nil && (types.Implements(typ, lockerType) || types.Implements(types.NewPointer(typ), lockerType)) {
						if lockSelector, ok := parent.X.(*ast.SelectorExpr); ok {
							if lockVar, ok := l.pass.TypesInfo.ObjectOf(lockSelector.Sel).(*types.Var); ok {
								if obj.Name() == "Lock" {
									fmt.Println("Locking", lockVar)
									l.locks[lockVar] = append(l.locks[lockVar], lockSelector)
								} else {
									fmt.Println("Unlocking", lockVar)
									edited := make([]*ast.SelectorExpr, 0)
									for _, heldLockSelector := range l.locks[lockVar] {
										if !expressionsMatch(l.pass, heldLockSelector.X, lockSelector.X) {
											edited = append(edited, heldLockSelector)
										}
									}
									l.locks[lockVar] = edited
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
		l.analyzeStmt(expr.Body)
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

type protectionsFinder struct {
	protections map[types.Object]*types.Var
}

func (f *protectionsFinder) find(pass *analysis.Pass, ins *inspector.Inspector) {
	ins.Preorder([]ast.Node{(*ast.StructType)(nil)}, func(n ast.Node) {
		structType := n.(*ast.StructType)

		strct, ok := pass.TypesInfo.TypeOf(structType).(*types.Struct)
		if !ok {
			return
		}

		for _, field := range structType.Fields.List {
			if field.Tag != nil {
				protectedByValue, ok := reflect.StructTag(strings.Trim(field.Tag.Value, "`")).Lookup("protected_by")
				if !ok {
					continue
				}

				lockExpr, err := parser.ParseExpr(protectedByValue)
				if err != nil {
					pass.Reportf(field.Tag.ValuePos, "couldn't parse protected_by expression: %v", err)
					continue
				}

				lockVar := findLockVar(strct, lockExpr)
				if lockVar == nil {
					pass.Reportf(field.Tag.ValuePos, "expression doesn't locate a lock field")
					continue
				}

				if !types.Implements(lockVar.Type(), lockerType) && !types.Implements(types.NewPointer(lockVar.Type()), lockerType) {
					pass.Reportf(field.Tag.ValuePos, "value referred to by expression doesn't implement sync.Locker")
					continue
				}

				for _, name := range field.Names {
					if vr, ok := pass.TypesInfo.ObjectOf(name).(*types.Var); vr != nil && ok {
						fmt.Println(vr, "is protected by", lockVar)

						f.protections[vr] = lockVar

						// Export protection info as a fact to other packages.
						if name.IsExported() {
							pass.ExportObjectFact(vr, &protectedBy{lock: lockVar})
						}
					}
				}
			}
		}
	})
}

// TODO make this work for function expressions, global lock variables (global context) & embedded fields.
// TODO what happens when we add generics to the picture?
func findLockVar(context *types.Struct, expr ast.Expr) *types.Var {
	switch expr := expr.(type) {
	case *ast.SelectorExpr:
		return findField(findLockVarContext(context, expr.X), expr.Sel.Name)
	case *ast.Ident:
		return findField(context, expr.Name)
	}
	return nil
}

func findLockVarContext(rootContext *types.Struct, expr ast.Expr) *types.Struct {
	switch expr := expr.(type) {
	case *ast.SelectorExpr:
		if parentContext := findLockVarContext(rootContext, expr.X); parentContext != nil {
			return findFieldStructType(parentContext, expr.Sel.Name)
		}
	case *ast.Ident:
		return findFieldStructType(rootContext, expr.Name)
	}
	return nil
}

func findFieldStructType(context *types.Struct, name string) *types.Struct {
	if field := findField(context, name); field != nil {
		if strct, ok := field.Type().Underlying().(*types.Struct); ok {
			return strct
		}
	}
	return nil
}

func findField(context *types.Struct, name string) *types.Var {
	for field := range context.Fields() {
		if field.Name() == name {
			return field
		}
	}
	return nil
}
