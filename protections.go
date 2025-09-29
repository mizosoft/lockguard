package lockgaurd

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/types"
	"iter"
	"reflect"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ast/inspector"
)

type protection struct {
	lockObj              types.Object // The function or variable locating the lock.
	lockExpr             ast.Expr
	lockExprWithReceiver ast.Expr // The original lock expression in guarded functions.
}

func (p *protection) String() string {
	return p.lockObj.Name()
}

type protectedBy struct {
	prot protection
}

func (p *protectedBy) AFact() {}

func (p *protectedBy) String() string {
	return fmt.Sprintf("protected_by:\"%s\"", p.prot.lockObj.Name())
}

type protectionsFinder struct {
	protections map[types.Object]protection
	pass        *analysis.Pass
}

func (f *protectionsFinder) find(ins *inspector.Inspector) {
	ins.Root().Inspect([]ast.Node{(*ast.StructType)(nil), (*ast.FuncDecl)(nil)}, func(c inspector.Cursor) (descend bool) {
		switch n := c.Node().(type) {
		case *ast.StructType:
			if specCursor, ok := lastOf(c.Enclosing((*ast.TypeSpec)(nil))); ok {
				f.findStructProtections(n, specCursor.Node().(*ast.TypeSpec))
			} else {
				f.findStructProtections(n, nil)
			}
		case *ast.FuncDecl:
			f.findFuncProtection(n)
		}
		return true
	})
}

func (f *protectionsFinder) findStructProtections(structType *ast.StructType, spec *ast.TypeSpec) {
	strct, ok := f.pass.TypesInfo.TypeOf(structType).(*types.Struct)
	if !ok {
		return
	}

	var def *types.Named
	if spec != nil {
		def = findStructDefinition(spec, f.pass)
	}

	for _, field := range structType.Fields.List {
		if field.Tag != nil {
			protectedByValue, ok := reflect.StructTag(strings.Trim(field.Tag.Value, "`")).Lookup("protected_by")
			if !ok {
				continue
			}

			prot, err := f.findProtection(strct, def, protectedByValue)
			if err != nil {
				f.pass.Reportf(field.Tag.ValuePos, "%v", err)
				return
			}

			for _, name := range field.Names {
				if vr, ok := f.pass.TypesInfo.ObjectOf(name).(*types.Var); vr != nil && ok {
					f.protections[vr] = prot

					// Export protection info as a fact to other packages.
					if name.IsExported() {
						f.pass.ExportObjectFact(vr, &protectedBy{prot: prot})
					}
				}
			}
		}
	}
}

func (f *protectionsFinder) findFuncProtection(funcType *ast.FuncDecl) {
	// The function must have one receiver that is a struct.
	if funcType.Recv.NumFields() != 1 {
		return
	}

	fnc, ok := f.pass.TypesInfo.ObjectOf(funcType.Name).(*types.Func)
	if !ok {
		return
	}

	receiver, ok := f.pass.TypesInfo.ObjectOf(funcType.Recv.List[0].Names[0]).(*types.Var)
	if !ok {
		return
	}

	receiverType, ok := removePointer(receiver.Type()).(*types.Named)
	if !ok {
		return
	}

	receiverStruct, ok := removePointer(receiverType).Underlying().(*types.Struct)
	if !ok {
		return
	}

	if funcType.Doc != nil {
		for _, comment := range funcType.Doc.List {
			if protectedByValue, ok := parseDirective(comment.Text); ok {
				// TODO we'll need to change this if we support embedded types (where the receiver is itself the lock)
				normalizedValue, ok := strings.CutPrefix(protectedByValue, receiver.Name()+".")
				if !ok {
					f.pass.Reportf(comment.Pos(), "expression doesn't locate a lock field")
					return
				}

				prot, err := f.findProtection(receiverStruct, receiverType, normalizedValue)
				if err != nil {
					f.pass.Reportf(comment.Pos(), "%v", err)
					return
				}

				lockExprWithReceiver, err := parser.ParseExpr(protectedByValue)
				if err != nil {
					f.pass.Reportf(comment.Pos(), "%v", err)
					return
				}

				prot.lockExprWithReceiver = lockExprWithReceiver

				f.protections[fnc] = prot

				// Export protection info as a fact to other packages.
				if funcType.Name.IsExported() {
					f.pass.ExportObjectFact(fnc, &protectedBy{prot: prot})
				}
			}
		}
	}
}

func (f *protectionsFinder) findProtection(context *types.Struct, contextDef *types.Named, value string) (protection, error) {
	lockExpr, err := parser.ParseExpr(value)
	if err != nil {
		return protection{}, fmt.Errorf("couldn't parse protected_by expression (%s): %v", value, err)
	}

	lockObj := f.findExprObj(context, contextDef, lockExpr, false)
	switch lockObj := lockObj.(type) {
	case *types.Func:
		if lockObj.Signature().Results().Len() != 1 || !isLocker(lockObj.Signature().Results().At(0).Type(), false) {
			return protection{}, errors.New("value referred to by expression doesn't implement sync.Locker")
		}
	case *types.Var:
		if !isLocker(lockObj.Type(), true) {
			return protection{}, errors.New("value referred to by expression doesn't implement sync.Locker")
		}
	default:
		return protection{}, errors.New("expression doesn't locate a lock field")
	}

	return protection{
		lockObj:  lockObj,
		lockExpr: lockExpr,
	}, nil
}

// TODO make this work global lock variables (global context) & embedded fields.
// TODO what happens when we add generics to the picture?
func (f *protectionsFinder) findExprObj(context *types.Struct, contextDef *types.Named, expr ast.Expr, inCall bool) types.Object {
	switch expr := expr.(type) {
	case *ast.Ident:
		if inCall {
			return findFunc(contextDef, expr.Name)
		} else {
			return findField(context, expr.Name)
		}
	case *ast.SelectorExpr:
		if inCall {
			if _, parentContextDef := findLockObjContext(context, contextDef, expr.X, false); parentContextDef != nil {
				return findFunc(parentContextDef, expr.Sel.Name)
			}
		} else if parentContext, _ := findLockObjContext(context, contextDef, expr.X, false); parentContext != nil {
			return findField(parentContext, expr.Sel.Name)
		}
	case *ast.CallExpr:
		if len(expr.Args) == 0 {
			return f.findExprObj(context, contextDef, expr.Fun, true)
		}
	case *ast.ParenExpr:
		return f.findExprObj(context, contextDef, expr.X, false)
	}
	return nil
}

func findLockObjContext(rootContext *types.Struct, rootContextDef *types.Named, expr ast.Expr, inCall bool) (*types.Struct, *types.Named) {
	switch expr := expr.(type) {
	case *ast.Ident:
		if inCall {
			return findFuncReturnType(rootContextDef, expr.Name)
		} else {
			return findFieldStructType(rootContext, expr.Name)
		}
	case *ast.SelectorExpr:
		if inCall {
			if _, parentContextDef := findLockObjContext(rootContext, rootContextDef, expr.X, false); parentContextDef != nil {
				return findFuncReturnType(parentContextDef, expr.Sel.Name)
			}
		} else if parentContext, _ := findLockObjContext(rootContext, rootContextDef, expr.X, false); parentContext != nil {
			return findFieldStructType(parentContext, expr.Sel.Name)
		}
	case *ast.CallExpr:
		return findLockObjContext(rootContext, rootContextDef, expr.Fun, true)
	case *ast.ParenExpr:
		return findLockObjContext(rootContext, rootContextDef, expr.X, inCall)
	}
	return nil, nil
}

func findFuncReturnType(contextDef *types.Named, name string) (*types.Struct, *types.Named) {
	if contextDef == nil {
		return nil, nil
	}

	if fnc := findFunc(contextDef, name); fnc != nil {
		if fnc.Signature().Results().Len() == 1 {
			typ := removePointer(fnc.Signature().Results().At(0).Type())
			strct, _ := typ.Underlying().(*types.Struct)
			def, _ := typ.(*types.Named)
			return strct, def
		}
	}
	return nil, nil
}

func findFieldStructType(context *types.Struct, name string) (*types.Struct, *types.Named) {
	if context == nil {
		return nil, nil
	}

	if field := findField(context, name); field != nil {
		typ := removePointer(field.Type())
		strct, _ := typ.Underlying().(*types.Struct)
		def, _ := typ.(*types.Named)
		return strct, def
	}
	return nil, nil
}

func findField(context *types.Struct, name string) *types.Var {
	if context == nil {
		return nil
	}

	for field := range context.Fields() {
		if field.Name() == name {
			return field
		}
	}
	return nil
}

func findFunc(contextDef *types.Named, name string) *types.Func {
	if contextDef == nil {
		return nil
	}

	for method := range contextDef.Methods() {
		if method.Name() == name {
			return method
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

func lastOf[T any](seq iter.Seq[T]) (T, bool) {
	var last T
	var ok = false
	for v := range seq {
		last = v
		ok = true
	}
	return last, ok
}

func findStructDefinition(spec *ast.TypeSpec, pass *analysis.Pass) *types.Named {
	if typeName, ok := pass.TypesInfo.ObjectOf(spec.Name).(*types.TypeName); ok {
		if def, ok := typeName.Type().(*types.Named); ok {
			return def
		}
	}
	return nil
}
