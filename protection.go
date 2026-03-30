package lockgaurd

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
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
)

var protectionDirectives = []protectionDirective{
	protectedBy, readProtectedBy, writeProtectedBy,
}

func (directive protectionDirective) isSupportedBy(kind lockKind) bool {
	switch directive {
	case protectedBy:
		return kind == rwLockKind || kind == normalLockKind
	case readProtectedBy, writeProtectedBy:
		return kind == rwLockKind
	default:
		panic("unknown protectionDirective: " + directive)
	}
}

func (directive protectionDirective) isSatisfiedBy(kind lockKind, isRLocked bool, fieldAccess accessKind) bool {
	switch directive {
	case protectedBy:
		switch kind {
		case normalLockKind:
			return true
		case rwLockKind:
			return fieldAccess == readAccessKind || !isRLocked
		default:
			return false
		}
	case readProtectedBy:
		return kind == rwLockKind // Either Lock or RLock would work here.
	case writeProtectedBy:
		return kind == rwLockKind && !isRLocked
	default:
		panic("unknown protectionDirective: " + directive)
	}
}

type protection struct {
	directive protectionDirective

	// The path locating the canonical lockObj.
	lockPath canonicalPath

	// True if the function receiver is the first object in lockPath.
	withReceiver bool
}

func (p protection) lockExprString() string {
	return p.lockPath.String()
}

func (p protection) String() string {
	return p.lockExprString()
}

func (p protection) lockObj() types.Object {
	return p.lockPath[len(p.lockPath)-1]
}

func (p protection) defaultLockUnlockFuncs() (string, string) {
	switch p.directive {
	case protectedBy, writeProtectedBy:
		return "Lock", "Unlock"
	case readProtectedBy:
		return "RLock", "RUnlock"
	default:
		panic("unknown protectionDirective: " + p.directive)
	}
}

type protectionFact struct {
	prots []protection
}

func (p protectionFact) AFact() {}

func (p protectionFact) String() string {
	return fmt.Sprintf("%v", p.prots)
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
	ins.Root().Inspect([]ast.Node{(*ast.StructType)(nil), (*ast.GenDecl)(nil), (*ast.FuncDecl)(nil)},
		func(c inspector.Cursor) bool {
			var file *ast.File
			if fileCursor, ok := lastOf(c.Enclosing((*ast.File)(nil))); ok {
				file = fileCursor.Node().(*ast.File)
			}

			switch n := c.Node().(type) {
			case *ast.StructType:
				// Find the containing definition (TypeSpec) if any.
				if specCursor, ok := lastOf(c.Enclosing((*ast.TypeSpec)(nil))); ok {
					f.findStructProtections(n, specCursor.Node().(*ast.TypeSpec), f.enclosingScopeOf(specCursor), file)
				} else {
					f.findStructProtections(n, nil, f.enclosingScopeOf(c), file)
				}
			case *ast.GenDecl:
				f.findVarDeclProtections(n, file)
			case *ast.FuncDecl:
				f.findFuncProtections(n, file)
			}
			return true
		})
}

func (f *protectionsFinder) enclosingScopeOf(c inspector.Cursor) *types.Scope {
	if scopeNode, ok := lastOf(c.Parent().Enclosing(
		(*ast.File)(nil),
		(*ast.FuncType)(nil),
		(*ast.TypeSpec)(nil),
		(*ast.BlockStmt)(nil),
		(*ast.IfStmt)(nil),
		(*ast.SwitchStmt)(nil),
		(*ast.TypeSwitchStmt)(nil),
		(*ast.CaseClause)(nil),
		(*ast.CommClause)(nil),
		(*ast.ForStmt)(nil),
		(*ast.RangeStmt)(nil),
	)); ok {
		return f.pass.TypesInfo.Scopes[scopeNode.Node()]
	}
	return nil
}

func (f *protectionsFinder) findStructProtections(typ *ast.StructType, spec *ast.TypeSpec, enclosingScope *types.Scope, file *ast.File) {
	strct, ok := f.pass.TypesInfo.TypeOf(typ).(*types.Struct)
	if !ok {
		return
	}

	var def *types.Named
	if spec != nil {
		def = f.findStructDefinition(spec)
	}

	loc := structLocator(strct, def).fallback(scopeLocator(enclosingScope)).fallback(importsLocator(file, f.pass.TypesInfo))
	for _, field := range typ.Fields.List {
		var prots []protection
		if field.Tag != nil {
			for _, directive := range protectionDirectives {
				value, ok := reflect.StructTag(strings.Trim(field.Tag.Value, "`")).Lookup(string(directive))
				if !ok {
					continue
				}

				prot, err := f.findProtection(loc, directive, value)
				if err != nil {
					f.pass.Reportf(field.Tag.ValuePos, "%v", err)
				} else {
					prots = append(prots, prot)
				}
			}

			if len(prots) == 0 {
				continue
			}

			if len(field.Names) > 0 {
				for _, name := range field.Names {
					if vr, ok := f.pass.TypesInfo.ObjectOf(name).(*types.Var); vr != nil && ok {
						f.protections[vr] = prots
						if debug {
							fmt.Println(vr, "protected by", fmt.Sprintf("%v", f.protections[vr]))
						}

						// Export protection info as a fact to other packages.
						if name.IsExported() {
							f.pass.ExportObjectFact(vr, &protectionFact{
								prots: prots,
							})
						}
					}
				}
			} else {
				// This is an embedded field.
				name := nameOfEmbeddedField(field.Type)
				if vr, ok := f.pass.TypesInfo.ObjectOf(name).(*types.Var); vr != nil && ok {
					f.protections[vr] = prots
					if debug {
						fmt.Println(vr, "protected by", fmt.Sprintf("%v", f.protections[vr]))
					}

					// Export protection info as a fact to other packages.
					if name.IsExported() {
						f.pass.ExportObjectFact(vr, &protectionFact{
							prots: prots,
						})
					}
				}
			}
		}
	}
}

func (f *protectionsFinder) findFuncProtections(funcType *ast.FuncDecl, file *ast.File) {
	if funcType.Doc == nil {
		return
	}

	fnc, ok := f.pass.TypesInfo.ObjectOf(funcType.Name).(*types.Func)
	if !ok {
		return
	}

	globalLocator := scopeLocator(f.pass.Pkg.Scope()).fallback(importsLocator(file, f.pass.TypesInfo))

	var receiver *types.Var
	if funcType.Recv != nil && len(funcType.Recv.List) > 0 && len(funcType.Recv.List[0].Names) > 0 {
		receiver = f.pass.TypesInfo.ObjectOf(funcType.Recv.List[0].Names[0]).(*types.Var)
	}

	for _, comment := range funcType.Doc.List {
		if directive, value, ok := parseCommentDirective(comment.Text); ok {
			var loc locator
			var withReceiver bool
			if receiver != nil {
				if localizedValue, ok := strings.CutPrefix(value, receiver.Name()+"."); ok {
					loc = objLocator(receiver)
					value = localizedValue
					withReceiver = true
				}
			}

			if loc == nil {
				loc = globalLocator
			}

			prot, err := f.findProtection(loc, directive, value)
			if err != nil {
				f.pass.Reportf(comment.Pos(), "%v", err)
				continue
			}
			prot.withReceiver = withReceiver

			f.protections[fnc] = append(f.protections[fnc], prot)
		}
	}

	// Export protection info as a fact to other packages.
	if len(f.protections[fnc]) > 0 {
		if debug {
			fmt.Println(fnc, "protected by", fmt.Sprintf("%v", f.protections[fnc]))
		}

		if funcType.Name.IsExported() {
			f.pass.ExportObjectFact(fnc, &protectionFact{
				prots: f.protections[fnc],
			})
		}
	}
}

func (f *protectionsFinder) findVarDeclProtections(decl *ast.GenDecl, file *ast.File) {
	if decl.Tok != token.VAR {
		return
	}

	// Simulate symbol lookup by first looking into package scope, then look into imports for package names.
	loc := scopeLocator(f.pass.Pkg.Scope()).fallback(importsLocator(file, f.pass.TypesInfo))

	var declProts []protection
	if decl.Doc != nil {
		for _, comment := range decl.Doc.List {
			if directive, value, ok := parseCommentDirective(comment.Text); ok {
				if prot, err := f.findProtection(loc, directive, value); err != nil {
					f.pass.Reportf(comment.Pos(), "%v", err)
				} else {
					declProts = append(declProts, prot)
				}
			}
		}
	}

	for _, spec := range decl.Specs {
		spec := spec.(*ast.ValueSpec)
		specProts := declProts
		if spec.Doc != nil {
			for _, comment := range spec.Doc.List {
				if directive, value, ok := parseCommentDirective(comment.Text); ok {
					prot, err := f.findProtection(loc, directive, value)
					if err != nil {
						f.pass.Reportf(comment.Pos(), "%v", err)
					} else {
						specProts = append(specProts, prot)
					}
				}
			}
		}

		if len(specProts) > 0 {
			for _, name := range spec.Names {
				if vr, ok := f.pass.TypesInfo.ObjectOf(name).(*types.Var); ok {
					f.protections[vr] = specProts
					if debug {
						fmt.Println(vr, "protected by", fmt.Sprintf("%v", specProts))
					}

					// Export protection info as a fact to other packages.
					if name.IsExported() {
						f.pass.ExportObjectFact(vr, &protectionFact{
							prots: specProts,
						})
					}
				}
			}
		}
	}
}

func (f *protectionsFinder) findProtection(l locator, directive protectionDirective, value string) (protection, error) {
	lockExpr, err := parser.ParseExpr(value)
	if err != nil {
		return protection{}, fmt.Errorf("couldn't parse protected_by expression (%s): %v", value, err)
	}

	lockPath := l.canonicalize(lockExpr)
	if lockPath == nil {
		return protection{}, fmt.Errorf("expression doesn't locate a lock field %s", value)
	}

	lockObj := lockPath[len(lockPath)-1]
	if !directive.isSupportedBy(lockKindOfObject(lockObj)) {
		return protection{}, fmt.Errorf("selected object's type doesn't satisfy %b", lockObj)
	}

	return protection{
		directive: directive,
		lockPath:  lockPath,
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
