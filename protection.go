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

func (directive protectionDirective) isSatisfiedBy(kind lockKind, isRead bool, access accessKind) bool {
	switch directive {
	case protectedBy:
		switch kind {
		case normalLockKind:
			return true
		case rwLockKind:
			return access == readAccessKind || !isRead
		default:
			return false
		}
	case readProtectedBy, rwProtectedBy:
		return kind == rwLockKind // Either Lock or RLock would work here.
	case writeProtectedBy:
		return kind == rwLockKind && !isRead
	default:
		panic("unknown protectionDirective: " + directive)
	}
}

type protection struct {
	directive protectionDirective

	// The function or variable locating the lock. Note that this is not necessarily the last object located by
	// the lockExpr. In case of embedded fields, this will be the canonical object on which the Lock/Unlock methods
	// are defined.
	lockObj types.Object

	// The path locating the canonical lockObj.
	lockPath canonicalPath

	// The expression specified by the directive.
	lockExpr ast.Expr

	// Only non-nil for guarded functions with receivers.
	lockExprWithReceiver ast.Expr
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

func (p *protection) defaultLockUnlockFuncs() (string, string) {
	switch p.directive {
	case protectedBy, writeProtectedBy:
		return "Lock", "Unlock"
	case readProtectedBy, rwProtectedBy:
		return "RLock", "RUnlock"
	default:
		panic("unknown protectionDirective: " + p.directive)
	}
}

type protectionFact struct {
	prot protection
}

func (p *protectionFact) AFact() {}

func (p *protectionFact) String() string {
	return p.prot.String()
}

type protectionsFinder struct {
	protections map[types.Object][]protection
	pass        *analysis.Pass
}

func newFinder(pass *analysis.Pass) protectionsFinder {
	return protectionsFinder{
		protections: make(map[types.Object][]protection),
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

				if len(field.Names) > 0 {
					for _, name := range field.Names {
						if vr, ok := f.pass.TypesInfo.ObjectOf(name).(*types.Var); vr != nil && ok {
							f.protections[vr] = append(f.protections[vr], prot)
							fmt.Println(vr, "protected by", prot.lockPath.String())

							// Export protection info as a fact to other packages.
							if name.IsExported() {
								f.pass.ExportObjectFact(vr, &protectionFact{
									prot: prot,
								})
							}
						}
					}
				} else {
					// This is an embedded field.
					name := nameOfEmbeddedField(field.Type)
					if vr, ok := f.pass.TypesInfo.ObjectOf(name).(*types.Var); vr != nil && ok {
						f.protections[vr] = append(f.protections[vr], prot)
						fmt.Println(vr, "protected by", prot.lockPath.String())

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

func nameOfEmbeddedField(typ ast.Expr) *ast.Ident {
	switch typ := typ.(type) {
	case *ast.Ident:
		return typ
	case *ast.SelectorExpr:
		return typ.Sel
	case *ast.StarExpr:
		return nameOfEmbeddedField(typ.X)
	default:
		panic("unexpected expr: " + types.ExprString(typ))
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

				f.protections[fnc] = append(f.protections[fnc], prot)

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

	lockPath := canonicalizeFrom(context, contextDef, lockExpr, false)
	if lockPath == nil {
		return protection{}, fmt.Errorf("invalid expression %s", directive)
	}

	lockObj := lockPath[len(lockPath)-1]
	if !directive.isSupportedBy(lockKindOfObject(lockObj)) {
		return protection{}, fmt.Errorf("selected object's type doesn't satisfy %s", directive)
	}

	return protection{
		directive: directive,
		lockPath:  lockPath,
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

func lastOf[T any](seq iter.Seq[T]) (T, bool) {
	var last T
	var ok = false
	for v := range seq {
		last = v
		ok = true
	}
	return last, ok
}
