package lockgaurd

import (
	"go/ast"
	"go/types"
	"slices"

	"golang.org/x/tools/go/analysis"
)

// The full path to follow from some type context to locate an object (a field or a method).
type canonicalPath []types.Object

func (c canonicalPath) String() string {
	str := ""
	for _, obj := range c {
		if len(str) > 0 {
			str += "."
		}
		str += "(" + obj.String() + ")"
	}
	return str
}

func canonicalize(expr ast.Expr, pass *analysis.Pass, inCall bool) canonicalPath {
	switch expr := expr.(type) {
	case *ast.Ident:
		if obj := pass.TypesInfo.ObjectOf(expr); obj != nil {
			return canonicalPath{obj}
		}
	case *ast.SelectorExpr:
		if parentPath := canonicalize(expr.X, pass, false); parentPath != nil {
			context, contextDef := typeOf(parentPath[len(parentPath)-1])
			if subPath := canonicalizeFrom(context, contextDef, expr.Sel, inCall); subPath != nil {
				return append(parentPath, subPath...)
			}
		}
	case *ast.CallExpr:
		return canonicalize(expr.Fun, pass, true)
	case *ast.ParenExpr:
		return canonicalize(expr.X, pass, inCall)
	}
	return nil
}

func canonicalizeFrom(rootContext *types.Struct, rootContextDef *types.Named, expr ast.Expr, inCall bool) canonicalPath {
	switch expr := expr.(type) {
	case *ast.Ident:
		if _, path := findObjWithPath(rootContext, rootContextDef, expr.Name, inCall); path != nil {
			return path
		}
	case *ast.SelectorExpr:
		if parentPath := canonicalizeFrom(rootContext, rootContextDef, expr.X, false); parentPath != nil {
			parentContext, parentContextDef := typeOf(parentPath[len(parentPath)-1])
			if subPath := canonicalizeFrom(parentContext, parentContextDef, expr.Sel, inCall); subPath != nil {
				return append(parentPath, subPath...)
			}
		}
	case *ast.CallExpr:
		return canonicalizeFrom(rootContext, rootContextDef, expr.Fun, true)
	case *ast.ParenExpr:
		return canonicalizeFrom(rootContext, rootContextDef, expr.X, inCall)
	}
	return nil
}

func findRootIdent(expr ast.Expr) (*ast.Ident, bool) {
	switch expr := expr.(type) {
	case *ast.BadExpr:
		return nil, false
	case *ast.CallExpr:
		return findRootIdent(expr.Fun)
	case *ast.Ident:
		return expr, true
	case *ast.IndexExpr:
		return findRootIdent(expr.X)
	case *ast.ParenExpr:
		return findRootIdent(expr.X)
	case *ast.SelectorExpr:
		return findRootIdent(expr.X)
	case *ast.SliceExpr:
		return findRootIdent(expr.X)
	case *ast.StarExpr:
		return findRootIdent(expr.X)
	case *ast.UnaryExpr:
		return findRootIdent(expr.X)
	case nil:
		return nil, true
	default:
		return nil, false
	}
}

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

func findObjWithPath(rootContext *types.Struct, rootContextDef *types.Named, name string, inCall bool) (types.Object, canonicalPath) {
	if inCall {
		return findFuncWithPath(rootContext, rootContextDef, name)
	} else {
		return findFieldWithPath(rootContext, name)
	}
}

func findFieldWithPath(rootContext *types.Struct, name string) (*types.Var, canonicalPath) {
	if rootContext == nil {
		return nil, nil
	}

	q := make([]*types.Var, 0)

	// This map serves a two-fold purpose: tracking visited fields so we don't endlessly follow
	// cycles, and recording which fields led to which so we can construct the path.
	parent := make(map[*types.Var]*types.Var)

	for field := range rootContext.Fields() {
		if field.Name() == name { // Short-circuit.
			return field, canonicalPath{field}
		} else {
			q = append(q, field)
			parent[field] = nil // nil here signifies an imaginary root.
		}
	}

	// Begin multi-source BFS, level-by-level.
	for len(q) > 0 {
		var matchedField *types.Var
		for ln := len(q); ln > 0; ln-- {
			field := q[0]
			q = q[1:]
			if field.Name() == name {
				if matchedField == nil {
					matchedField = field
				} else {
					return nil, nil // Ambiguous reference.
				}
			} else if field.Embedded() {
				// Follow path
				context, _ := fieldTypeOf(field)
				if context == nil { // Can't complete the search.
					return nil, nil
				}
				for subField := range context.Fields() {
					if _, ok := parent[subField]; !ok {
						q = append(q, subField)
						parent[subField] = field
					}
				}
			}
		}

		if matchedField != nil { // Found field.
			var path canonicalPath
			for curr := matchedField; curr != (*types.Var)(nil); curr = parent[curr] {
				path = append(path, curr)
			}
			slices.Reverse(path)
			return matchedField, path
		}
	}
	return nil, nil
}

func findFunc(rootContext *types.Struct, rootContextDef *types.Named, name string) *types.Func {
	fnc, _ := findFuncWithPath(rootContext, rootContextDef, name)
	return fnc
}

func findFuncWithPath(rootContext *types.Struct, rootContextDef *types.Named, name string) (*types.Func, canonicalPath) {
	if rootContext == nil || rootContextDef == nil {
		return nil, nil
	}

	q := make([]types.Object, 0)

	// This map serves a two-fold purpose: tracking visited fields so we don't endlessly follow
	// cycles, and recording which fields led to which so we can re-construct the path.
	parent := make(map[types.Object]*types.Var)

	for method := range rootContextDef.Methods() {
		if method.Name() == name { // Short-circuit.
			return method, canonicalPath{method}
		}
	}

	for field := range rootContext.Fields() {
		if field.Embedded() {
			q = append(q, field)
			parent[field] = nil // nil here signifies an imaginary root.
		}
	}

	// Begin multi-source BFS, level-by-level.
	for len(q) > 0 {
		var matchedMethod *types.Func
		for ln := len(q); ln > 0; ln-- {
			obj := q[0]
			q = q[1:]
			switch obj := obj.(type) {
			case *types.Func:
				if obj.Name() == name {
					if matchedMethod == nil {
						matchedMethod = obj
					} else {
						return nil, nil // Ambiguous reference.
					}
				}
			case *types.Var:
				// Follow path
				context, contextDef := fieldTypeOf(obj)
				if context == nil || contextDef == nil { // Can't complete the search.
					return nil, nil
				}

				for meth := range contextDef.Methods() {
					q = append(q, meth)
					parent[meth] = obj
				}

				for subField := range context.Fields() {
					if subField.Embedded() {
						if _, ok := parent[subField]; !ok {
							q = append(q, subField)
							parent[subField] = obj
						}
					}
				}
			}
		}

		if matchedMethod != nil { // Found field.
			var path canonicalPath
			for curr := types.Object(matchedMethod); curr != (*types.Var)(nil); curr = parent[curr] {
				path = append(path, curr)
			}
			slices.Reverse(path)
			return matchedMethod, path
		}
	}
	return nil, nil
}

func removePointer(typ types.Type) types.Type {
	if ptr, ok := typ.(*types.Pointer); ok {
		return ptr.Elem()
	}
	return typ
}
