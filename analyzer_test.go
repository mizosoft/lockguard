package lockgaurd

import (
	"go/parser"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
	"gotest.tools/v3/assert"
)

func TestAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, Analyzer, "a")
}

func TestTrimExpr(t *testing.T) {
	expr, err := parser.ParseExpr("x.y.k")
	assert.NilError(t, err)

	suffix1, err := parser.ParseExpr("y.k")
	assert.NilError(t, err)

	prefix1, err := parser.ParseExpr("x")
	assert.NilError(t, err)

	trim1, ok := trimSuffix(expr, suffix1)
	assert.Assert(t, ok)
	assert.Assert(t, expressionsMatch(trim1, prefix1))

	suffix2, err := parser.ParseExpr("k")
	assert.NilError(t, err)

	prefix2, err := parser.ParseExpr("x.y")
	assert.NilError(t, err)

	trim2, ok := trimSuffix(expr, suffix2)
	assert.Assert(t, ok)
	assert.Assert(t, expressionsMatch(trim2, prefix2))

	trim3, ok := trimSuffix(expr, expr)
	assert.Assert(t, ok)
	assert.Assert(t, trim3 == nil)

	trim4, ok := trimSuffix(expr, prefix1)
	assert.Assert(t, !ok)
	assert.Assert(t, trim4 == nil)
}

func TestDirectiveParsing(t *testing.T) {
	value, ok := parseDirective("//lockguard:protected_by s.mu")
	assert.Assert(t, ok)
	assert.Equal(t, value, "s.mu")

	value, ok = parseDirective("//lockguard:protected_by mu")
	assert.Assert(t, ok)
	assert.Equal(t, value, "mu")

	_, ok = parseDirective("//lockguard:protected_by")
	assert.Assert(t, !ok)

	_, ok = parseDirective("// lockguard:protected_by s.mu")
	assert.Assert(t, !ok)

	_, ok = parseDirective("/*lockguard:protected_by s.mu*/")
	assert.Assert(t, !ok)
}
