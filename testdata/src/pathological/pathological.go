// Package pathological pins the CFG shape that makes the DFS analysis explode combinatorially:
// long chains of sequential fall-through branches. Each such "diamond" (an if whose arms rejoin)
// doubles the number of entry→exit paths because the DFS forks at every branch and never re-merges
// at join points; K fall-through diamonds yield 2^K paths. Branches that terminate the function
// (return/panic) do not multiply — which is why huge-but-panicky functions like reflect.StructOf
// analyze instantly while these shapes hang.
//
// Both functions are distilled from real stdlib code found by per-function tracing of a hung run
// over the expvar package's dependency graph (2026-07-06):
//   - crypto/tls.(*clientHelloMsg).marshalMsg — ~21 sequential per-extension if-blocks, no early
//     returns, nested builder closures.
//   - encoding/json.(*decodeState).object — 57 branch points, only 19 of which terminate.
//
// This package is NOT part of TestAnalyzer's package "a": including it there would hang the whole
// suite. It is exercised by TestPathologicalCfg in analyzer_test.go, which bounds the run with a
// timeout and is gated behind LOCKGUARD_RUN_PATHOLOGICAL=1 until the path-explosion fix lands.
package pathological

import "sync"

type marshaler struct {
	mu    sync.Mutex
	data  int `protected_by:"mu"`
	flags [32]bool
}

// fallthroughChain is the minimal exponential shape: 26 sequential fall-through diamonds
// (2^26 ≈ 67M paths as of the current DFS; ~36s measured). The trailing unprotected access asserts
// that, once the analysis terminates, aggregation across all paths still works: every path misses
// the lock, so the diagnostic must be the certain form.
func (m *marshaler) fallthroughChain() int {
	x := 0
	if m.flags[0] {
		x++
	}
	if m.flags[1] {
		x++
	}
	if m.flags[2] {
		x++
	}
	if m.flags[3] {
		x++
	}
	if m.flags[4] {
		x++
	}
	if m.flags[5] {
		x++
	}
	if m.flags[6] {
		x++
	}
	if m.flags[7] {
		x++
	}
	if m.flags[8] {
		x++
	}
	if m.flags[9] {
		x++
	}
	if m.flags[10] {
		x++
	}
	if m.flags[11] {
		x++
	}
	if m.flags[12] {
		x++
	}
	if m.flags[13] {
		x++
	}
	if m.flags[14] {
		x++
	}
	if m.flags[15] {
		x++
	}
	if m.flags[16] {
		x++
	}
	if m.flags[17] {
		x++
	}
	if m.flags[18] {
		x++
	}
	if m.flags[19] {
		x++
	}
	if m.flags[20] {
		x++
	}
	if m.flags[21] {
		x++
	}
	if m.flags[22] {
		x++
	}
	if m.flags[23] {
		x++
	}
	if m.flags[24] {
		x++
	}
	if m.flags[25] {
		x++
	}
	m.data++ // want `writing 'm\.data' requires holding 'm\.mu'`
	return x
}

type extBuilder struct{ buf []byte }

func (b *extBuilder) add(f func(*extBuilder)) { f(b) }
func (b *extBuilder) put(x byte)              { b.buf = append(b.buf, x) }

// marshalExtensionsLike mirrors crypto/tls.(*clientHelloMsg).marshalMsg more faithfully:
// fall-through diamonds with && short-circuit conditions (each an extra CFG fork) and nested
// function-literal callbacks inside the arms (each re-analyzed in isolation on every path that
// reaches it, multiplying the per-path constant factor). No diagnostics expected.
func (m *marshaler) marshalExtensionsLike(echInner bool) []byte {
	var exts extBuilder
	if m.flags[0] {
		exts.add(func(e *extBuilder) { e.add(func(e *extBuilder) { e.put(1) }) })
	}
	if m.flags[1] && !echInner {
		exts.add(func(e *extBuilder) { e.add(func(e *extBuilder) { e.put(2) }) })
	}
	if m.flags[2] && !echInner {
		exts.add(func(e *extBuilder) { e.put(3) })
	}
	if m.flags[3] && !echInner {
		exts.add(func(e *extBuilder) { e.add(func(e *extBuilder) { e.put(4) }) })
	}
	if m.flags[4] && !echInner {
		exts.put(5)
	}
	if m.flags[5] {
		exts.put(6)
	}
	if m.flags[6] {
		exts.put(7)
	}
	if m.flags[7] {
		exts.add(func(e *extBuilder) { e.put(8) })
	}
	if m.flags[8] {
		exts.add(func(e *extBuilder) { e.put(9) })
	}
	if m.flags[9] {
		exts.add(func(e *extBuilder) { e.add(func(e *extBuilder) { e.put(10) }) })
	}
	if m.flags[10] && !echInner {
		exts.add(func(e *extBuilder) { e.add(func(e *extBuilder) { e.put(11) }) })
	}
	if m.flags[11] {
		exts.add(func(e *extBuilder) { e.add(func(e *extBuilder) { e.put(12) }) })
	}
	if m.flags[12] {
		exts.add(func(e *extBuilder) { e.add(func(e *extBuilder) { e.put(13) }) })
	}
	if m.flags[13] {
		exts.add(func(e *extBuilder) { e.add(func(e *extBuilder) { e.put(14) }) })
	}
	if m.flags[14] {
		exts.add(func(e *extBuilder) { e.add(func(e *extBuilder) { e.put(15) }) })
	}
	if m.flags[15] {
		exts.add(func(e *extBuilder) { e.add(func(e *extBuilder) { e.put(16) }) })
	}
	if m.flags[16] {
		exts.add(func(e *extBuilder) { e.add(func(e *extBuilder) { e.put(17) }) })
	}
	if m.flags[17] {
		exts.add(func(e *extBuilder) { e.put(18) })
	}
	if m.flags[18] {
		exts.add(func(e *extBuilder) { e.add(func(e *extBuilder) { e.put(19) }) })
	}
	if m.flags[19] {
		exts.add(func(e *extBuilder) { e.add(func(e *extBuilder) { e.put(20) }) })
	}
	if m.flags[20] && !echInner {
		exts.add(func(e *extBuilder) { e.add(func(e *extBuilder) { e.put(21) }) })
	}
	var outer []byte
	for _, b := range exts.buf {
		if b%2 == 0 {
			outer = append(outer, b)
		}
	}
	if len(outer) > 0 && echInner {
		exts.put(22)
	}
	if len(exts.buf) > 0 {
		exts.add(func(e *extBuilder) { e.put(23) })
	}
	return exts.buf
}