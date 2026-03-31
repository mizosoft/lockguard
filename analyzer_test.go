package lockguard

import (
	"fmt"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
	"gotest.tools/v3/assert"
)

func TestAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, Analyzer, "a")
}

func TestDirectiveParsing(t *testing.T) {
	for _, directive := range protectionDirectives {
		parsedDirective, value, ok := parseCommentDirective(fmt.Sprintf("//lockguard:%s s.mu", directive))
		assert.Assert(t, ok)
		assert.Equal(t, directive, parsedDirective)
		assert.Equal(t, value, "s.mu")
	}

	_, value, ok := parseCommentDirective("//lockguard:protected_by mu")
	assert.Assert(t, ok)
	assert.Equal(t, value, "mu")

	_, _, ok = parseCommentDirective("//lockguard:protected_by")
	assert.Assert(t, !ok)

	_, _, ok = parseCommentDirective("// lockguard:protected_by s.mu")
	assert.Assert(t, !ok)

	_, _, ok = parseCommentDirective("/*lockguard:protected_by s.mu*/")
	assert.Assert(t, !ok)

	_, _, ok = parseCommentDirective("//lockguard:guarded_by s.mu")
	assert.Assert(t, !ok)
}

// TODO add tests for canonical path finding.
