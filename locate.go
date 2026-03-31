package lockguard

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"
)

// The full path to follow from some type context to locate an object (a field or a method).
type canonicalPath []types.Object

func (c canonicalPath) String() string {
	parts := make([]string, len(c))
	for i, obj := range c {
		parts[i] = obj.Name()
	}
	return strings.Join(parts, ".")
}

type locator func(name *ast.Ident) canonicalPath

var nilLocator locator = func(name *ast.Ident) canonicalPath {
	return nil
}

func (l locator) fallback(f locator) locator {
	return func(name *ast.Ident) canonicalPath {
		if obj := l(name); obj != nil {
			return obj
		}
		return f(name)
	}
}

func objLocator(obj types.Object) locator {
	if obj == nil {
		return nilLocator
	}

	switch obj := obj.(type) {
	case *types.PkgName:
		return scopeLocator(obj.Imported().Scope())
	case *types.Var, *types.Func:
		typ, def := typeOf(obj)
		return structLocator(typ, def)
	}
	return nilLocator
}

func locateFromObj(obj types.Object, ident *ast.Ident) canonicalPath {
	return objLocator(obj)(ident)
}

func locateFromObjByName(obj types.Object, name string) canonicalPath {
	return locateFromObj(obj, &ast.Ident{
		Name: name,
	})
}

func structLocator(typ *types.Struct, def *types.Named) locator {
	if typ == nil && def == nil {
		return nilLocator
	}

	return func(name *ast.Ident) canonicalPath {
		if path := findStructObj(typ, def, name.Name); path != nil {
			return path
		}
		return nil
	}
}

func scopeLocator(scope *types.Scope) locator {
	if scope == nil {
		return nilLocator
	}

	return func(name *ast.Ident) canonicalPath {
		if _, obj := scope.LookupParent(name.Name, token.NoPos); obj != nil {
			return canonicalPath{obj}
		}
		return nil
	}
}

func importsLocator(file *ast.File, info *types.Info) locator {
	if file == nil {
		return nilLocator
	}

	return func(name *ast.Ident) canonicalPath {
		for _, spec := range file.Imports {
			if pkgName := info.PkgNameOf(spec); pkgName != nil && pkgName.Name() == name.Name {
				return canonicalPath{pkgName}
			}
		}
		return nil
	}
}

func infoLocator(info *types.Info) locator {
	if info == nil {
		return nilLocator
	}

	return func(name *ast.Ident) canonicalPath {
		if obj := info.ObjectOf(name); obj != nil {
			return canonicalPath{obj}
		}
		return nil
	}
}

func (l locator) canonicalize(expr ast.Expr) canonicalPath {
	switch expr := expr.(type) {
	case *ast.Ident:
		return l(expr)
	case *ast.SelectorExpr:
		if parentPath := l.canonicalize(expr.X); parentPath != nil {
			if subPath := objLocator(parentPath[len(parentPath)-1])(expr.Sel); subPath != nil {
				return append(parentPath, subPath...)
			}
		}
	case *ast.CallExpr:
		return l.canonicalize(expr.Fun)
	case *ast.ParenExpr:
		return l.canonicalize(expr.X)
	}
	return nil
}

// TODO make findField/findFunc return the partial path if lookup failed, to present a proper error.

func typeOf(obj types.Object) (*types.Struct, *types.Named) {
	switch obj := obj.(type) {
	case *types.Func:
		return returnTypeOf(obj)
	case *types.Var:
		return fieldTypeOf(obj)
	default:
		return nil, nil
	}
}

func returnTypeOf(fnc *types.Func) (*types.Struct, *types.Named) {
	if fnc == nil {
		return nil, nil
	}

	if fnc.Signature().Results().Len() == 1 {
		typ := removePointer(fnc.Signature().Results().At(0).Type())
		strct, _ := typ.Underlying().(*types.Struct)
		def, _ := typ.(*types.Named)
		return strct, def
	}
	return nil, nil
}

func fieldTypeOf(field *types.Var) (*types.Struct, *types.Named) {
	if field == nil {
		return nil, nil
	}

	typ := removePointer(field.Type())
	strct, _ := typ.Underlying().(*types.Struct)
	def, _ := typ.(*types.Named)
	return strct, def
}

func findStructObj(rootTyp *types.Struct, rootDef *types.Named, name string) canonicalPath {
	if rootTyp == nil {
		return nil
	}

	q := make([]*types.Var, 0)

	// This map serves a two-fold purpose: tracking visited fields so we don't endlessly follow
	// cycles, and recording which fields led to which so we can construct the path.
	parent := make(map[*types.Var]*types.Var)

	for field := range rootTyp.Fields() {
		if field.Name() == name { // Short-circuit.
			return canonicalPath{field}
		}
	}

	if rootDef != nil {
		for method := range rootDef.Methods() {
			if method.Name() == name {
				return canonicalPath{method} // Short-circuit
			}
		}
	}

	for field := range rootTyp.Fields() {
		if field.Embedded() {
			q = append(q, field)
		}
	}

	// Begin multi-source BFS, level-by-level.
	for len(q) > 0 {
		var matchedObj types.Object
		var matchedObjOwner *types.Var
		for ln := len(q) - 1; ln >= 0; ln-- {
			field := q[0]
			q = q[1:]

			typ, def := fieldTypeOf(field)
			if typ == nil {
				continue
			}

			for subField := range typ.Fields() {
				if subField.Name() == name { // Short-circuit.
					if matchedObj == nil {
						matchedObj = subField // Ambiguous reference.
						matchedObjOwner = field
					} else {
						return nil
					}
				}
			}

			if rootDef != nil {
				for method := range def.Methods() {
					if method.Name() == name {
						if matchedObj == nil {
							matchedObj = method
							matchedObjOwner = field
						} else {
							return nil
						}
					}
				}
			}

			for subField := range typ.Fields() {
				if subField.Embedded() {
					q = append(q, subField)
					parent[subField] = field
				}
			}

			if matchedObj != nil { // Found field.
				path := canonicalPath{matchedObj, matchedObjOwner}
				curr, ok := parent[matchedObjOwner]
				for ok {
					path = append(path, curr)
					curr, ok = parent[curr]
				}
				slices.Reverse(path)
				return path
			}
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
