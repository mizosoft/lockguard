package lockguard

import (
	"go/token"

	"golang.org/x/tools/go/analysis"
)

// Category classifies a Lockguard diagnostic. Drivers and linting tools can
// use it to filter or suppress specific classes of finding independently of
// the message text. When a diagnostic has no suggested fix, the URL field of
// analysis.Diagnostic is left empty and tools should treat it as "#"+Category.
type Category string

const (
	// CategoryMissingLock is emitted when a protected variable is accessed
	// without the required lock definitely held on all paths.
	CategoryMissingLock Category = "missing-lock"

	// CategoryPossiblyMissingLock is emitted when the required lock is not
	// held on all paths reaching the access (e.g. acquired only in one branch
	// of an if statement).
	CategoryPossiblyMissingLock Category = "possibly-missing-lock"

	// CategoryDeadlock is emitted when a lock is acquired while it is already
	// definitely held, which would block forever on a non-reentrant mutex.
	CategoryDeadlock Category = "deadlock"

	// CategoryPossibleDeadlock is emitted when a lock is acquired (via
	// TryLock) while it may already be held on some paths.
	CategoryPossibleDeadlock Category = "possible-deadlock"

	// CategoryInvalidUnlock is emitted when Unlock or RUnlock is called on a
	// lock that is not currently held (certain or possible).
	CategoryInvalidUnlock Category = "invalid-unlock"

	// CategoryInvalidAnnotation is emitted when a Lockguard annotation —
	// a struct tag or a //lockguard: comment directive — cannot be parsed or
	// resolved to a known lock object.
	CategoryInvalidAnnotation Category = "invalid-annotation"
)

// lockDiagnostic pairs a Category with a human-readable message. Functions in
// scope.go return slices of these; analyzer.go converts them into
// analysis.Diagnostic values at the appropriate source position.
type lockDiagnostic struct {
	category Category
	message  string
}

// reportAll emits each diagnostic at pos via pass.Report, setting the Category
// field so that consumers can classify findings without parsing message text.
func reportAll(pass *analysis.Pass, pos token.Pos, diags []lockDiagnostic) {
	for _, d := range diags {
		pass.Report(analysis.Diagnostic{
			Pos:      pos,
			Category: string(d.category),
			Message:  d.message,
		})
	}
}
