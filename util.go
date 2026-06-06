package lockguard

import (
	"cmp"
	"go/ast"
	"go/types"
	"iter"
	"path"
	"slices"
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

func filterType[T any, E any](s []E) []T {
	var result []T
	for _, v := range s {
		if t, ok := any(v).(T); ok {
			result = append(result, t)
		}
	}
	return result
}

func compareBy[E any, C cmp.Ordered](extractor func(e E) C) func(a, b E) int {
	return func(a, b E) int {
		aCmp := extractor(a)
		bCmp := extractor(b)
		if aCmp < bCmp {
			return -1
		} else if aCmp > bCmp {
			return 1
		} else {
			return 0
		}
	}
}

func sortedBy[E any, C cmp.Ordered](s iter.Seq[E], extractor func(e E) C) []E {
	return slices.SortedFunc(s, compareBy(extractor))
}

func allMatch[E any](els []E, f func(e E) bool) bool {
	for _, e := range els {
		if !f(e) {
			return false
		}
	}
	return true
}

func anyMatch[E any](els []E, f func(e E) bool) bool {
	for _, e := range els {
		if f(e) {
			return true
		}
	}
	return false
}

func matchCount[E any](els []E, f func(e E) bool) int {
	cnt := 0
	for _, e := range els {
		if f(e) {
			cnt++
		}
	}
	return cnt
}
