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
	// TODO we want this to eventually work on arbitrary expressions.
	lock *types.Var
}

func (p *protectedBy) AFact() {}

func (p *protectedBy) String() string {
	return fmt.Sprintf("protected_by:\"%s\"", p.lock.Name())
}

func run(pass *analysis.Pass) (interface{}, error) {
	if pass.Pkg.Name() != "a" {
		return nil, nil
	}

	ins, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		return nil, nil
	}

	// Maps var objects in structs to var objects with type sync.Mutex that should protect them.
	protections := make(map[*types.Var]*types.Var)

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

			// TODO make this work for embedded types.

			// Find sync.Mutex fields in this struct.
			lockFields := make(map[string]*types.Var)
			for _, field := range structType.Fields.List {
				if fieldType, isNamed := pass.TypesInfo.TypeOf(field.Type).(*types.Named); isNamed {
					if fieldType.String() == "sync.Mutex" {
						// This is a sync.Mutex field. TODO is this check enough? Can't we have a similarly named type?
						for _, name := range field.Names {
							if obj := pass.TypesInfo.ObjectOf(name); obj != nil {
								if varObj, isVar := obj.(*types.Var); isVar {
									lockFields[name.Name] = varObj
								}
							}
						}
					}
				}
			}

			for _, field := range structType.Fields.List {
				if field.Tag != nil {
					protectedByFieldName, ok := reflect.StructTag(strings.Trim(field.Tag.Value, "`")).Lookup("protected_by")
					if !ok {
						continue
					}

					lockVar, lockExists := lockFields[protectedByFieldName]
					if !lockExists {
						pass.Reportf(field.Pos(), "No sync.Mutex field with name <%s> exists", protectedByFieldName)
						return
					}

					for _, name := range field.Names {
						if obj := pass.TypesInfo.ObjectOf(name); obj != nil {
							if varObj, isVar := obj.(*types.Var); isVar {
								protections[varObj] = lockVar
								//fmt.Printf("%s is protected by %s\n", varObj.Origin(), protectedByExpr)

								// Export protection info as a fact to other packages.
								if name.IsExported() {
									pass.ExportObjectFact(varObj, &protectedBy{lock: lockVar})
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
					if lockVar, isProtected := protections[varObj]; isProtected {
						fmt.Printf("%s is protected by %s\n", varObj, lockVar)
					}
				}
			}
		}
	})

	return nil, nil
}
