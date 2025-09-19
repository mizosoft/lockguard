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
		heldLocks:   make(map[*types.Var][]ast.Expr),
	}
	ins.Preorder([]ast.Node{(ast.Decl)(nil)}, func(node ast.Node) {
		l.analyzeDecl(pass, node.(ast.Decl))
	})

	return nil, nil
}

type lockAnalyzer struct {
	protections map[types.Object]*types.Var
	heldLocks   map[*types.Var][]ast.Expr
}

// TODO handle global vars when we allow protection specs by comments.
func (l *lockAnalyzer) analyzeStmt(pass *analysis.Pass, stmt ast.Stmt) {
	switch stmt := stmt.(type) {
	case *ast.DeclStmt:
		l.analyzeDecl(pass, stmt.Decl)
	case *ast.LabeledStmt:
		l.analyzeStmt(pass, stmt.Stmt)
	case *ast.ExprStmt:
		l.analyzeExpr(pass, stmt.X, nil)
	case *ast.SendStmt:
		l.analyzeExpr(pass, stmt.Chan, nil)
		l.analyzeExpr(pass, stmt.Value, nil)
	case *ast.IncDecStmt:
		l.analyzeExpr(pass, stmt.X, nil)
	case *ast.AssignStmt:
		l.analyzeExprs(pass, stmt.Lhs)
		l.analyzeExprs(pass, stmt.Rhs)
	case *ast.GoStmt:
		l.analyzeExpr(pass, stmt.Call, nil)
	case *ast.DeferStmt:
		l.analyzeExpr(pass, stmt.Call, nil)
	case *ast.ReturnStmt:
		l.analyzeExprs(pass, stmt.Results)
	case *ast.BlockStmt:
		for _, stmt := range stmt.List {
			l.analyzeStmt(pass, stmt)
		}
	case *ast.IfStmt:
		l.analyzeStmt(pass, stmt.Init)
		l.analyzeExpr(pass, stmt.Cond, nil)
		l.analyzeStmt(pass, stmt.Body)
		l.analyzeStmt(pass, stmt.Else)
	case *ast.CaseClause:
		l.analyzeExprs(pass, stmt.List)
		for _, innerStmt := range stmt.Body {
			l.analyzeStmt(pass, innerStmt)
		}
	case *ast.SwitchStmt:
		l.analyzeStmt(pass, stmt.Init)
		l.analyzeExpr(pass, stmt.Tag, nil)
		l.analyzeStmt(pass, stmt.Body)
	case *ast.TypeSwitchStmt:
		l.analyzeStmt(pass, stmt.Init)
		l.analyzeStmt(pass, stmt.Assign)
		l.analyzeStmt(pass, stmt.Body)
	case *ast.CommClause:
		l.analyzeStmt(pass, stmt.Comm)
		for _, innerStmt := range stmt.Body {
			l.analyzeStmt(pass, innerStmt)
		}
	case *ast.SelectStmt:
		l.analyzeStmt(pass, stmt.Body)
	case *ast.ForStmt:
		l.analyzeStmt(pass, stmt.Init)
		l.analyzeExpr(pass, stmt.Cond, nil)
		l.analyzeStmt(pass, stmt.Post)
		l.analyzeStmt(pass, stmt.Body)
	case *ast.RangeStmt:
		l.analyzeExpr(pass, stmt.X, nil)
		l.analyzeStmt(pass, stmt.Body)
	case *ast.BadStmt, *ast.EmptyStmt, *ast.BranchStmt:
		// Skip
	}
}

func (l *lockAnalyzer) analyzeDecl(pass *analysis.Pass, decl ast.Decl) {
	switch decl := decl.(type) {
	case *ast.FuncDecl:
		// TODO we need to have the receiver as context.
		l.analyzeStmt(pass, decl.Body)
	case *ast.GenDecl:
		if decl.Tok == token.VAR {
			for _, spec := range decl.Specs {
				if valueSpec, isValueSpec := spec.(*ast.ValueSpec); isValueSpec {
					l.analyzeExprs(pass, valueSpec.Values)
				}
			}
		}
	case *ast.BadDecl:
		// Skip
	}
}

func (l *lockAnalyzer) analyzeExprs(pass *analysis.Pass, exprs []ast.Expr) {
	for _, expr := range exprs {
		l.analyzeExpr(pass, expr, nil)
	}
}

func (l *lockAnalyzer) analyzeExpr(pass *analysis.Pass, expr ast.Expr, parent ast.Expr) {
	switch expr := expr.(type) {
	case *ast.Ident:
		if varObj, ok := pass.TypesInfo.ObjectOf(expr).(*types.Var); ok {
			if lockVar, ok := l.protections[varObj]; ok {
				if parentAsSelector, ok := parent.(*ast.SelectorExpr); ok {
					for _, lockHoldingExpr := range l.heldLocks[lockVar] {
						if expressionsMatch(pass, parentAsSelector.X, lockHoldingExpr) {
							fmt.Println("Lock", lockVar, "is held while accessing", varObj)
						} else {
							fmt.Println("Lock", lockVar, "is not held while accessing", varObj)
						}
					}
				}
			}
		}
	case *ast.Ellipsis:
		l.analyzeExpr(pass, expr.Elt, expr)
	case *ast.FuncLit:
		l.analyzeStmt(pass, expr.Body)
	case *ast.CompositeLit:
		for _, el := range expr.Elts {
			l.analyzeExpr(pass, el, expr)
		}
	case *ast.ParenExpr:
		l.analyzeExpr(pass, expr.X, expr)
	case *ast.SelectorExpr:
		l.analyzeExpr(pass, expr.X, expr)
		l.analyzeExpr(pass, expr.Sel, expr)
	case *ast.IndexExpr:
		l.analyzeExpr(pass, expr.X, expr)
		l.analyzeExpr(pass, expr.Index, expr)
	case *ast.IndexListExpr:
		l.analyzeExpr(pass, expr.X, expr)
		for _, ind := range expr.Indices {
			l.analyzeExpr(pass, ind, expr)
		}
	case *ast.SliceExpr:
		l.analyzeExpr(pass, expr.X, expr)
		l.analyzeExpr(pass, expr.Low, expr)
		l.analyzeExpr(pass, expr.High, expr)
		l.analyzeExpr(pass, expr.Max, expr)
	case *ast.TypeAssertExpr:
		l.analyzeExpr(pass, expr.X, expr)
	case *ast.CallExpr:
		l.analyzeExpr(pass, expr.Fun, expr)

		// TODO handle function checks when we allow protecting them via comments.

		// Check if this call is a Lock() call.
		if funcSelector, isSelector := expr.Fun.(*ast.SelectorExpr); isSelector {
			if exprType := pass.TypesInfo.TypeOf(funcSelector.X); exprType != nil && exprType.String() == "sync.Mutex" {
				if lockSelector, ok := funcSelector.X.(*ast.SelectorExpr); ok {
					if lockVar := pass.TypesInfo.ObjectOf(lockSelector.Sel).(*types.Var); lockVar != nil {
						if funcSelector.Sel.Name == "Lock" {
							fmt.Println("Locking", lockVar)
							l.heldLocks[lockVar] = append(l.heldLocks[lockVar], lockSelector.X)
						} else if funcSelector.Sel.Name == "Unlock" {
							var updatedExprs []ast.Expr
							for _, holdingExpr := range l.heldLocks[lockVar] {
								if !expressionsMatch(pass, lockSelector.X, holdingExpr) {
									updatedExprs = append(updatedExprs, holdingExpr)
								} else {
									fmt.Println("Unlocking", lockVar)
								}
							}
							l.heldLocks[lockVar] = updatedExprs
						}
					}
				}
			}
		}

		for _, arg := range expr.Args {
			l.analyzeExpr(pass, arg, expr)
		}
	case *ast.StarExpr:
		l.analyzeExpr(pass, expr.X, expr)
	case *ast.UnaryExpr:
		l.analyzeExpr(pass, expr.X, expr)
	case *ast.BinaryExpr:
		l.analyzeExpr(pass, expr.X, expr)
		l.analyzeExpr(pass, expr.Y, expr)
	case *ast.KeyValueExpr:
		// We're not interested in the key.
		l.analyzeExpr(pass, expr.Value, expr)
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

	fmt.Println(reflect.TypeOf(left), reflect.TypeOf(right))

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
						f.protections[vr] = lockVar

						// Export protection info as a fact to other packages.
						//if name.IsExported() {
						//pass.ExportObjectFact(vr, &protectedBy{lock: lockVar})
						//}
					}
				}
			}
		}
	})
}

// TODO make this work for function expressions, global lock variables (global context) & embedded fields.
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
