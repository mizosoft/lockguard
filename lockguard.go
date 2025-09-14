package main

import (
	"fmt"
	"go/ast"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
	"reflect"
	"strings"
)

// Analyzer Checks lock-protected accesses.
var Analyzer = &analysis.Analyzer{
	Name:      "astcheck",
	Doc:       "Checks lock-protected accesses",
	Run:       run,
	Requires:  []*analysis.Analyzer{inspect.Analyzer},
	FactTypes: []analysis.Fact{new(protectedBy)},
}

type protectedBy struct {
	expr string
}

func (p *protectedBy) AFact() {}

func (p *protectedBy) String() string {
	return fmt.Sprintf("protected_by:\"%s\"", p.expr)
}

func run(pass *analysis.Pass) (interface{}, error) {
	ins, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		return nil, nil
	}

	// Scan the tree for lock protections.
	ins.Preorder([]ast.Node{(*ast.GenDecl)(nil)}, func(n ast.Node) {
		genDecl := n.(*ast.GenDecl)
		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}

			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}

			for _, field := range structType.Fields.List {
				protectedByExpr, ok := reflect.StructTag(strings.Trim(field.Tag.Value, "`")).Lookup("protected_by")
				if !ok {
					continue
				}

				for _, name := range field.Names {
					obj := pass.TypesInfo.ObjectOf(name)
					if obj == nil {
						continue
					}

					// Export protection info to other packages.
					if name.IsExported() {
						pass.ExportObjectFact(obj, &protectedBy{expr: protectedByExpr})
					}
				}
			}
		}

		// Scan to check accesses.
		ins.Preorder([]ast.Node{(*ast.GenDecl)(nil)}, func(n ast.Node) {

		})
	})

	return nil, nil
}
