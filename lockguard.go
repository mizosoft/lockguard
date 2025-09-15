package main

import (
	"fmt"
	"go/ast"
	"go/types"
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
	if pass.Pkg.Name() != "a" {
		return nil, nil
	}

	ins, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		return nil, nil
	}

	protections := map[*types.Var]string{}

	// Scan the tree for lock protections.
	ins.Preorder([]ast.Node{(*ast.GenDecl)(nil)}, func(n ast.Node) {
		for _, spec := range n.(*ast.GenDecl).Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}

			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}

			for _, field := range structType.Fields.List {
				if field.Tag != nil {
					protectedByExpr, ok := reflect.StructTag(strings.Trim(field.Tag.Value, "`")).Lookup("protected_by")
					if !ok {
						continue
					}

					for _, name := range field.Names {
						if obj := pass.TypesInfo.ObjectOf(name); obj != nil {
							if varObj, isVar := obj.(*types.Var); isVar {
								protections[varObj] = protectedByExpr
								//fmt.Printf("%s is protected by %s\n", varObj.Origin(), protectedByExpr)

								// Export protection info as a fact to other packages.
								if name.IsExported() {
									pass.ExportObjectFact(varObj, &protectedBy{expr: protectedByExpr})
								}
							}
						}
					}
				}
			}
		}
	})

	ins.Preorder([]ast.Node{(*ast.SelectorExpr)(nil)}, func(n ast.Node) {
		selectorExpr := n.(*ast.SelectorExpr)
		if exprType := pass.TypesInfo.TypeOf(selectorExpr.X); exprType != nil {
			if obj := pass.TypesInfo.ObjectOf(selectorExpr.Sel); obj != nil {
				if varObj, isVar := obj.(*types.Var); isVar {
					if protection, isProtected := protections[varObj]; isProtected {
						fmt.Printf("%s is protected by %s\n", varObj.Origin(), protection)
					}
				}
			}
		}
	})

	return nil, nil
}
