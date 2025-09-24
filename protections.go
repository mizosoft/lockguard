package lockgaurd

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/types"
	"reflect"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ast/inspector"
)

type protection struct {
	lockVar  *types.Var
	lockExpr ast.Expr
}

func (p *protection) String() string {
	return p.lockVar.Name()
}

type protectedBy struct {
	prot protection
}

func (p *protectedBy) AFact() {}

func (p *protectedBy) String() string {
	return fmt.Sprintf("protected_by:\"%s\"", p.prot.lockVar.Name())
}

type protectionsFinder struct {
	protections map[types.Object]protection
}

func (f *protectionsFinder) find(pass *analysis.Pass, ins *inspector.Inspector) {
	ins.Preorder([]ast.Node{(*ast.StructType)(nil), (*ast.FuncDecl)(nil)}, func(n ast.Node) {
		switch n := n.(type) {
		case *ast.StructType:
			f.findStructProtections(pass, n)
		case *ast.FuncDecl:
			f.findFuncProtection(pass, n)
		}
	})
}

func (f *protectionsFinder) findStructProtections(pass *analysis.Pass, structType *ast.StructType) {
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

			prot, err := findProtection(strct, protectedByValue)
			if err != nil {
				pass.Reportf(field.Tag.ValuePos, "%v", err)
				return
			}

			for _, name := range field.Names {
				if vr, ok := pass.TypesInfo.ObjectOf(name).(*types.Var); vr != nil && ok {
					f.protections[vr] = prot

					// Export protection info as a fact to other packages.
					if name.IsExported() {
						pass.ExportObjectFact(vr, &protectedBy{prot: prot})
					}
				}
			}
		}
	}
}

func (f *protectionsFinder) findFuncProtection(pass *analysis.Pass, funcType *ast.FuncDecl) {
	// The function must have one receiver that is a struct.
	if funcType.Recv.NumFields() != 1 {
		return
	}

	fnc, ok := pass.TypesInfo.ObjectOf(funcType.Name).(*types.Func)
	if !ok {
		return
	}

	receiver, ok := pass.TypesInfo.ObjectOf(funcType.Recv.List[0].Names[0]).(*types.Var)
	if !ok {
		return
	}

	receiverStruct, ok := removePointer(receiver.Type()).Underlying().(*types.Struct)
	if !ok {
		return
	}

	if funcType.Doc != nil {
		for _, comment := range funcType.Doc.List {
			if protectedByValue, ok := parseDirective(comment.Text); ok {
				// TODO we'll need to change this if we support embedded types (where the receiver is itself the lock)
				normalizedValue, ok := strings.CutPrefix(protectedByValue, receiver.Name()+".")
				if !ok {
					pass.Reportf(comment.Pos(), "expression doesn't locate a lock field")
					return
				}

				prot, err := findProtection(receiverStruct, normalizedValue)
				if err != nil {
					pass.Reportf(comment.Pos(), "%v", err)
					return
				}

				f.protections[fnc] = prot

				// Export protection info as a fact to other packages.
				if funcType.Name.IsExported() {
					pass.ExportObjectFact(fnc, &protectedBy{prot: prot})
				}
			}
		}
	}
}

func findProtection(context *types.Struct, value string) (protection, error) {
	lockExpr, err := parser.ParseExpr(value)
	if err != nil {
		return protection{}, fmt.Errorf("couldn't parse protected_by expression (%s): %v", value, err)
	}

	lockVar := findLockVar(context, lockExpr)
	if lockVar == nil {
		return protection{}, errors.New("expression doesn't locate a lock field")
	}

	if !types.Implements(lockVar.Type(), lockerType) && !types.Implements(types.NewPointer(lockVar.Type()), lockerType) {
		return protection{}, errors.New("value referred to by expression doesn't implement sync.Locker")
	}

	return protection{
		lockVar:  lockVar,
		lockExpr: lockExpr,
	}, nil
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
		if strct, ok := removePointer(field.Type()).Underlying().(*types.Struct); ok {
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

func findLockVarThroughVar(context *types.Var, expr ast.Expr) *types.Var {
	switch expr := expr.(type) {
	case *ast.SelectorExpr:
		if parentVar := findLockVarThroughVar(context, expr.X); parentVar != nil {
			if strct, ok := removePointer(parentVar.Type()).Underlying().(*types.Struct); ok {
				return findField(strct, expr.Sel.Name)
			}
		}
	case *ast.Ident:
		if context.Name() == expr.Name {
			return context
		}
	}
	return nil
}

func removePointer(typ types.Type) types.Type {
	if ptr, ok := typ.(*types.Pointer); ok {
		return ptr.Elem()
	}
	return typ
}
