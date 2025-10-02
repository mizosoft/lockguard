package lockgaurd

import (
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

type protectionDirective string

const (
	protectedBy      protectionDirective = "protected_by"
	readProtectedBy  protectionDirective = "read_protected_by"
	writeProtectedBy protectionDirective = "write_protected_by"
	rwProtectedBy    protectionDirective = "rw_protected_by"
)

var protectionDirectives = []protectionDirective{
	protectedBy, readProtectedBy, writeProtectedBy, rwProtectedBy,
}

func (directive protectionDirective) isSupportedBy(kind lockKind) bool {
	switch directive {
	case protectedBy:
		return kind == rwLockKind || kind == normalLockKind
	case readProtectedBy, writeProtectedBy, rwProtectedBy:
		return kind == rwLockKind
	default:
		panic("unknown protectionDirective: " + directive)
	}
}

func (directive protectionDirective) isSatisfiedBy(lock heldLock, access accessKind) bool {
	switch directive {
	case protectedBy:
		switch lock.kind {
		case normalLockKind:
			return true
		case rwLockKind:
			return access == readAccessKind || !lock.isRead
		default:
			return false
		}
	case readProtectedBy, rwProtectedBy:
		return lock.kind == rwLockKind // Either Lock or RLock would work here.
	case writeProtectedBy:
		return lock.kind == rwLockKind && !lock.isRead
	default:
		panic("unknown protectionDirective: " + directive)
	}
}

type protection struct {
	directive            protectionDirective
	lockObj              types.Object // The function or variable locating the lock.
	lockExpr             ast.Expr
	lockExprWithReceiver ast.Expr // Only non-nil for guarded functions with receivers.
}

func (p *protection) lockExprString() string {
	if p.lockExprWithReceiver != nil {
		return types.ExprString(p.lockExprWithReceiver)
	} else {
		return types.ExprString(p.lockExpr)
	}
}

func (p *protection) String() string {
	return p.lockExprString()
}

type protectionFact struct {
	prot protection
}

func (p *protectionFact) AFact() {}

func (p *protectionFact) String() string {
	return p.prot.String()
}

type protectionsMap struct {
	mp map[types.Object][]protection
}

func (p *protectionsMap) get(obj types.Object, directive protectionDirective) []protection {
	var prots []protection
	for _, prot := range p.mp[obj] {
		if prot.directive == directive {
			prots = append(prots, prot)
		}
	}
	return prots
}

func (p *protectionsMap) getAll(obj types.Object) []protection {
	return p.mp[obj]
}

func (p *protectionsMap) put(obj types.Object, prot protection) {
	p.mp[obj] = append(p.mp[obj], prot)
}

func newProtectionsMap() protectionsMap {
	return protectionsMap{
		mp: make(map[types.Object][]protection),
	}
}

type protectionsFinder struct {
	protections protectionsMap
	pass        *analysis.Pass
}

func newFinder(pass *analysis.Pass) protectionsFinder {
	return protectionsFinder{
		protections: newProtectionsMap(),
		pass:        pass,
	}
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
		def = f.findStructDefinition(spec)
	}

	for _, field := range structType.Fields.List {
		if field.Tag != nil {
			for _, directive := range protectionDirectives {
				value, ok := reflect.StructTag(strings.Trim(field.Tag.Value, "`")).Lookup(string(directive))
				if !ok {
					continue
				}

				prot, err := f.findProtection(strct, def, directive, value)
				if err != nil {
					f.pass.Reportf(field.Tag.ValuePos, "%v", err)
					return
				}

				for _, name := range field.Names {
					if vr, ok := f.pass.TypesInfo.ObjectOf(name).(*types.Var); vr != nil && ok {
						f.protections.put(vr, prot)

						// Export protection info as a fact to other packages.
						if name.IsExported() {
							f.pass.ExportObjectFact(vr, &protectionFact{
								prot: prot,
							})
						}
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

	receiverDef, ok := removePointer(receiver.Type()).(*types.Named)
	if !ok {
		return
	}

	receiverStruct, ok := removePointer(receiverDef).Underlying().(*types.Struct)
	if !ok {
		return
	}

	if funcType.Doc != nil {
		for _, comment := range funcType.Doc.List {
			if kind, value, ok := parseCommentDirective(comment.Text); ok {
				// TODO we'll need to change this if we support embedded types (where the receiver is itself the lock)
				normalizedValue, ok := strings.CutPrefix(value, receiver.Name()+".")
				if !ok {
					f.pass.Reportf(comment.Pos(), "expression doesn't locate a lock field")
					return
				}

				prot, err := f.findProtection(receiverStruct, receiverDef, kind, normalizedValue)
				if err != nil {
					f.pass.Reportf(comment.Pos(), "%v", err)
					return
				}

				lockExprWithReceiver, err := parser.ParseExpr(value)
				if err != nil {
					f.pass.Reportf(comment.Pos(), "%v", err)
					return
				}

				prot.lockExprWithReceiver = lockExprWithReceiver

				f.protections.put(fnc, prot)

				// Export protection info as a fact to other packages.
				if funcType.Name.IsExported() {
					f.pass.ExportObjectFact(fnc, &protectionFact{
						prot: prot,
					})
				}
			}
		}
	}
}

func (f *protectionsFinder) findProtection(context *types.Struct, contextDef *types.Named, directive protectionDirective, value string) (protection, error) {
	lockExpr, err := parser.ParseExpr(value)
	if err != nil {
		return protection{}, fmt.Errorf("couldn't parse protected_by expression (%s): %v", value, err)
	}

	lockObj := f.findExprObj(context, contextDef, lockExpr, false)
	if !directive.isSupportedBy(lockKindOfObject(lockObj)) {
		return protection{}, fmt.Errorf("selected object's type doesn't satisfied %s", directive)
	}

	return protection{
		directive: directive,
		lockObj:   lockObj,
		lockExpr:  lockExpr,
	}, nil
}

func (f *protectionsFinder) findStructDefinition(spec *ast.TypeSpec) *types.Named {
	if typeName, ok := f.pass.TypesInfo.ObjectOf(spec.Name).(*types.TypeName); ok {
		if def, ok := typeName.Type().(*types.Named); ok {
			return def
		}
	}
	return nil
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
