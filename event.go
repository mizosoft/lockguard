package lockguard

import (
	"fmt"
	"go/token"
	"go/types"
	"slices"
	"strconv"
	"strings"
)

type event interface {
	pos() token.Pos

	// A string representation of the canonicalPath of the target object. This coding is
	// needed as multiple objects can be associated with the same token.Pos in case of embedded
	// fields that have an implicit position in source code.
	pathCode() string
}

type baseEvent struct {
	mPos      token.Pos
	mPathCode string
}

func (e baseEvent) pos() token.Pos {
	return e.mPos
}

func (e baseEvent) pathCode() string {
	return e.mPathCode
}

type accessEvent struct {
	baseEvent
	access             accessKind
	missingProtections []missingProtection
}

type lockEvent interface {
	event

	isUncertain() bool

	isRLock() bool
}

type baseLockEvent struct {
	baseEvent
	uncertain bool
	rlock     bool
}

func (e *baseLockEvent) isUncertain() bool {
	return e.uncertain
}

func (e *baseLockEvent) isRLock() bool {
	return e.rlock
}

type acquireEvent struct {
	baseLockEvent
	deadlock bool
}

func (e *acquireEvent) isUncertain() bool {
	return e.uncertain
}

type releaseEvent struct {
	baseLockEvent
	invalid bool
}

type leakEvent struct {
	baseLockEvent
	acquirePos token.Pos
}

type canonicalPathCoder struct {
	ids        map[types.Object]int
	reverseIds map[int]types.Object
	nextId     int
}

func newCannicalPathCoder() *canonicalPathCoder {
	return &canonicalPathCoder{
		ids:        make(map[types.Object]int),
		reverseIds: make(map[int]types.Object),
		nextId:     0,
	}
}

// Returns a string that identifies this canonical path taking into account the identity
// of the associated types.Object. Can be used to make identity string map keys out of canonicalPaths.
func (enc *canonicalPathCoder) encode(path canonicalPath) string {
	parts := make([]string, 0, len(path))
	for _, obj := range path {
		var id int
		if localId, ok := enc.ids[obj]; ok {
			id = localId
		} else {
			id = enc.nextId
			enc.ids[obj] = id
			enc.reverseIds[id] = obj
			enc.nextId++
		}
		parts = append(parts, strconv.Itoa(id))
	}
	return strings.Join(parts, "/")
}

// Reverse of encode.
func (enc *canonicalPathCoder) decode(pathCode string) canonicalPath {
	var path canonicalPath
	for _, part := range strings.Split(pathCode, "/") {
		id, err := strconv.Atoi(part)
		if err != nil {
			panic(err)
		}

		obj, ok := enc.reverseIds[id]
		if !ok {
			panic(fmt.Errorf("unknown id %d", id))
		}

		path = append(path, obj)
	}
	return path
}

type eventRecorder struct {
	events    map[token.Pos][]event
	cpCoder   *canonicalPathCoder
	exitPaths map[token.Pos]int
}

func newEventRecorder() *eventRecorder {
	return &eventRecorder{
		events:    make(map[token.Pos][]event),
		cpCoder:   newCannicalPathCoder(),
		exitPaths: make(map[token.Pos]int),
	}
}

func (r *eventRecorder) recordExitPath(pos token.Pos) {
	r.exitPaths[pos]++
}

func (r *eventRecorder) recordAcquire(pos token.Pos, path canonicalPath, rlock bool, uncertain bool, deadlock bool) {
	r.events[pos] = append(r.events[pos], &acquireEvent{
		baseLockEvent: baseLockEvent{
			baseEvent: baseEvent{
				mPos:      pos,
				mPathCode: r.cpCoder.encode(path),
			},
			rlock:     rlock,
			uncertain: uncertain,
		},
		deadlock: deadlock,
	})
}

func (r *eventRecorder) recordRelease(pos token.Pos, path canonicalPath, rlock bool, uncertain bool, invalid bool) {
	r.events[pos] = append(r.events[pos], &releaseEvent{
		baseLockEvent: baseLockEvent{
			baseEvent: baseEvent{
				mPos:      pos,
				mPathCode: r.cpCoder.encode(path),
			},
			rlock:     rlock,
			uncertain: uncertain,
		},
		invalid: invalid,
	})
}

func (r *eventRecorder) recordAccess(pos token.Pos, path canonicalPath, access accessKind, missingProtections []missingProtection) {
	r.events[pos] = append(r.events[pos], &accessEvent{
		baseEvent: baseEvent{
			mPos:      pos,
			mPathCode: r.cpCoder.encode(path),
		},
		access:             access,
		missingProtections: missingProtections,
	})
}

func (r *eventRecorder) recordLeak(pos token.Pos, path canonicalPath, uncertain bool, rlock bool, acquirePos token.Pos) {
	r.events[pos] = append(r.events[pos], &leakEvent{
		baseLockEvent: baseLockEvent{
			baseEvent: baseEvent{
				mPos:      pos,
				mPathCode: r.cpCoder.encode(path),
			},
			uncertain: uncertain,
			rlock:     rlock,
		},
		acquirePos: acquirePos,
	})
}

func (r *eventRecorder) gatherDiagnostics() (diags []lockDiagnostic) {
	for pos, events := range r.events {
		// Process access events.
		accessEventsByPath := make(map[string][]*accessEvent)
		for _, e := range filterType[*accessEvent](events) {
			accessEventsByPath[e.pathCode()] = append(accessEventsByPath[e.pathCode()], e)
		}
		for pathCode, evts := range accessEventsByPath {
			diags = append(diags, r.gatherAccessDiagnostics(pos, pathCode, evts)...)
		}

		// Process deadlock events.
		deadlockEventsByPath := make(map[string][]*acquireEvent)
		for _, e := range filterType[*acquireEvent](events) {
			deadlockEventsByPath[e.pathCode()] = append(deadlockEventsByPath[e.pathCode()], e)
		}
		for pathCode, evts := range deadlockEventsByPath {
			diags = append(diags, r.gatherDeadlockDiagnostics(pos, pathCode, evts)...)
		}

		// Process invalid release events.
		releaseEventsByPath := make(map[string][]*releaseEvent)
		for _, e := range filterType[*releaseEvent](events) {
			releaseEventsByPath[e.pathCode()] = append(releaseEventsByPath[e.pathCode()], e)
		}
		for pathCode, evts := range releaseEventsByPath {
			diags = append(diags, r.gatherInvalidReleaseDiagnostics(pos, pathCode, evts)...)
		}

		// Process lock-leak events.
		leakEventsByPath := make(map[string][]*leakEvent)
		for _, e := range filterType[*leakEvent](events) {
			leakEventsByPath[e.pathCode()] = append(leakEventsByPath[e.pathCode()], e)
		}
		for pathCode, evts := range leakEventsByPath {
			diags = append(diags, r.gatherLeakDiagnostics(pos, pathCode, evts)...)
		}
	}
	return
}

func (r *eventRecorder) gatherAccessDiagnostics(pos token.Pos, pathCode string, events []*accessEvent) (diags []lockDiagnostic) {
	if len(events) == 0 {
		return
	}

	// Report missing protections.
	missingLocks := make(map[string]int)
	for _, event := range events {
		for _, prot := range event.missingProtections {
			identity := r.cpCoder.encode(prot.lockPath)
			missingLocks[identity]++
		}
	}

	// Segregate missing protections depending on whether all paths lead to the protection
	// being missed or not.
	certainlyMissingLocks := make([]canonicalPath, 0)
	possiblyMissingLocks := make([]canonicalPath, 0)
	for lock, missCount := range missingLocks {
		if missCount == len(events) {
			// All paths miss this lock — but if any event's missing is uncertain (lock was possibly
			// held on that path via TryLock), the overall diagnosis is "possibly missing".
			anyUncertain := anyMatch(events, func(e *accessEvent) bool {
				return anyMatch(e.missingProtections, func(p missingProtection) bool {
					return r.cpCoder.encode(p.lockPath) == lock && p.uncertain
				})
			})
			if anyUncertain {
				possiblyMissingLocks = append(possiblyMissingLocks, r.cpCoder.decode(lock))
			} else {
				certainlyMissingLocks = append(certainlyMissingLocks, r.cpCoder.decode(lock))
			}
		} else { // Only some paths miss this lock.
			possiblyMissingLocks = append(possiblyMissingLocks, r.cpCoder.decode(lock))
		}
	}

	// An object in the same position cannot have multiple access contexts.
	access := events[0].access
	for i := 1; i < len(events); i++ {
		if access != events[i].access {
			panic(fmt.Errorf("different accesses for the same object in the same position"))
		}
	}

	var verb string
	if access == writeAccessKind {
		verb = "writing"
	} else {
		verb = "reading"
	}

	fieldPath := r.cpCoder.decode(pathCode)
	fieldString := "'" + fieldPath.String() + "'"

	// lockPaths resolves each protection's lock relative to the object path prefix
	// and returns the full dot-joined path strings (e.g. "s.mu", "b.GlobalMut"), sorted for
	// deterministic diagnostic output.
	lockPaths := func(lockPaths []canonicalPath) []string {
		names := make([]string, len(lockPaths))
		for i, p := range lockPaths {
			names[i] = canonicalPath(copyAppend(fieldPath[:len(fieldPath)-1], p...)).String()
		}
		slices.Sort(names)
		return names
	}

	if len(certainlyMissingLocks) > 0 {
		diags = append(diags, lockDiagnostic{CategoryMissingLock, pos, fmt.Sprintf("%s %s requires holding %s", verb, fieldString, formatLockNames(lockPaths(certainlyMissingLocks)))})
	}
	if len(possiblyMissingLocks) > 0 {
		diags = append(diags, lockDiagnostic{CategoryPossiblyMissingLock, pos, fmt.Sprintf("%s %s requires holding %s (not held on all paths)", verb, fieldString, formatLockNames(lockPaths(possiblyMissingLocks)))})
	}
	return diags
}

func (r *eventRecorder) gatherDeadlockDiagnostics(pos token.Pos, pathCode string, events []*acquireEvent) (diags []lockDiagnostic) {
	if len(events) == 0 {
		return
	}

	deadlockCount := 0
	for _, e := range events {
		if e.deadlock {
			deadlockCount++
		}
	}

	if deadlockCount <= 0 {
		return
	}

	// Uncertain if not all paths deadlock (some are normal acquires), or any individual event is uncertain.
	uncertain := (deadlockCount < len(events)) || anyMatch(events, func(e *acquireEvent) bool { return e.uncertain })

	lockPath := r.cpCoder.decode(pathCode)
	lockName := "'" + lockPath[len(lockPath)-1].Name() + "'"
	if uncertain {
		diags = append(diags, lockDiagnostic{CategoryPossibleDeadlock, pos, fmt.Sprintf("acquiring %s may cause deadlock: may already be held", lockName)})
	} else {
		diags = append(diags, lockDiagnostic{CategoryDeadlock, pos, fmt.Sprintf("acquiring %s that is already held [deadlock]", lockName)})
	}
	return
}

func (r *eventRecorder) gatherInvalidReleaseDiagnostics(pos token.Pos, pathCode string, events []*releaseEvent) (diags []lockDiagnostic) {
	if len(events) == 0 {
		return
	}

	invalidReleaseCount := 0
	for _, e := range events {
		if e.invalid {
			invalidReleaseCount++
		}
	}

	if invalidReleaseCount <= 0 {
		return
	}

	// Uncertain if not all paths have an invalid release (some are valid), or any individual event is uncertain.
	uncertain := (invalidReleaseCount < len(events)) || anyMatch(events, func(e *releaseEvent) bool { return e.uncertain })

	lockPath := r.cpCoder.decode(pathCode)
	lockName := "'" + lockPath[len(lockPath)-1].Name() + "'"
	rlock := events[0].isRLock()
	if uncertain {
		if rlock {
			diags = append(diags, lockDiagnostic{CategoryPossiblyInvalidUnlock, pos, fmt.Sprintf("releasing read lock on %s that may not be held", lockName)})
		} else {
			diags = append(diags, lockDiagnostic{CategoryPossiblyInvalidUnlock, pos, fmt.Sprintf("releasing %s that may not be held", lockName)})
		}
	} else {
		if rlock {
			diags = append(diags, lockDiagnostic{CategoryInvalidUnlock, pos, fmt.Sprintf("releasing read lock on %s that is not held", lockName)})
		} else {
			diags = append(diags, lockDiagnostic{CategoryInvalidUnlock, pos, fmt.Sprintf("releasing %s that is not held", lockName)})
		}
	}
	return
}

func (r *eventRecorder) gatherLeakDiagnostics(pos token.Pos, pathCode string, events []*leakEvent) (diags []lockDiagnostic) {
	if len(events) == 0 {
		return
	}

	totalExits := r.exitPaths[pos]
	uncertain := anyMatch(events, func(e *leakEvent) bool { return e.uncertain }) ||
		(totalExits > 0 && len(events) < totalExits)
	lockName := "'" + r.cpCoder.decode(pathCode).String() + "'"
	rlock := events[0].isRLock()
	if uncertain {
		if rlock {
			diags = append(diags, lockDiagnostic{CategoryLockedAtExit, pos, fmt.Sprintf("read lock on %s may not be unlocked at function exit", lockName)})
		} else {
			diags = append(diags, lockDiagnostic{CategoryLockedAtExit, pos, fmt.Sprintf("%s may not be unlocked at function exit", lockName)})
		}
	} else {
		reportPos := events[0].acquirePos
		if !reportPos.IsValid() {
			reportPos = pos
		}
		if rlock {
			diags = append(diags, lockDiagnostic{CategoryLockedAtExit, reportPos, fmt.Sprintf("read lock on %s acquired but never unlocked", lockName)})
		} else {
			diags = append(diags, lockDiagnostic{CategoryLockedAtExit, reportPos, fmt.Sprintf("%s acquired but never unlocked", lockName)})
		}
	}
	return
}
