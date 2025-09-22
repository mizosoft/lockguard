package lockgaurd

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/types"
	"reflect"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ast/inspector"
)

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
