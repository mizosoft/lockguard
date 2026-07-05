package lockguard

import (
	"go/ast"
	"go/types"
	"iter"
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

func anyMatch[E any](els []E, f func(e E) bool) bool {
	for _, e := range els {
		if f(e) {
			return true
		}
	}
	return false
}
