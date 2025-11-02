package lockgaurd

import (
	"go/ast"
	"go/types"
	"iter"
	"path"
	"strings"
)

func lastOf[T any](seq iter.Seq[T]) (T, bool) {
	var last T
	var ok = false
	for v := range seq {
		last = v
		ok = true
	}
	return last, ok
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

func importsOf(file *ast.File) []string {
	if file == nil {
		return nil
	}

	var imports []string
	for _, spec := range file.Imports {
		if spec.Name != nil {
			// Named import: import foo "path/to/pkg"
			name := spec.Name.Name
			if name != "_" && name != "." {
				imports = append(imports, name)
			}
		} else {
			// Default import: extract package name from path
			imports = append(imports, path.Base(strings.Trim(spec.Path.Value, `"`)))
		}
	}
	return imports
}

func nillOf[T any]() T {
	var t T
	return t
}

// Used when the assigned slice is different from the slice appended to.
func copyAppend[T any](slice []T, elems ...T) []T {
	return append(append([]T(nil), slice...), elems...)
}
