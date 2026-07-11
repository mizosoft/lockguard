package lockguard

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/tools/go/analysis/analysistest"
	"gotest.tools/v3/assert"
)

func TestAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, Analyzer, "a")
}

// errorfRecorder satisfies analysistest.Testing so analysistest.Run can execute on a goroutine
// other than the test's without touching *testing.T from the wrong goroutine.
type errorfRecorder struct {
	mu   sync.Mutex
	errs []string
}

func (r *errorfRecorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

// TestPathologicalCfg pins the combinatorial-explosion limitation: the DFS forks at every branch
// and never re-merges at join points, so the fall-through diamond chains in testdata/src/pathological
// (distilled from crypto/tls.marshalMsg and encoding/json.object) take 2^K paths and effectively
// hang the analyzer. The test bounds the run with a timeout and fails on expiry.
//
// KNOWN-FAILING until the path-explosion fix lands, therefore gated: run it with
//
//	LOCKGUARD_RUN_PATHOLOGICAL=1 go test -run TestPathologicalCfg
//
// Once the fix is in, drop the gate so it runs (and passes, in well under the deadline) in CI.
func TestPathologicalCfg(t *testing.T) {
	if os.Getenv("LOCKGUARD_RUN_PATHOLOGICAL") == "" {
		t.Skip("known-failing (exponential DFS); set LOCKGUARD_RUN_PATHOLOGICAL=1 to run")
	}

	rec := &errorfRecorder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		analysistest.Run(rec, analysistest.TestData(), Analyzer, "pathological")
	}()

	select {
	case <-done:
		for _, e := range rec.errs {
			t.Error(e)
		}
	case <-time.After(10 * time.Second):
		// The analysis goroutine is abandoned; it dies with the test process.
		t.Fatal("analysis of testdata/src/pathological did not terminate within 10s (CFG path explosion)")
	}
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
