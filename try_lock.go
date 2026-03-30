package lockgaurd

import (
	"fmt"
	"slices"
)

type tryLockState int

const (
	trueTryLockState tryLockState = iota
	falseTryLockState
	unknownTryLockState
)

type tryLockCall struct {
	path    canonicalPath
	state   tryLockState
	isRLock bool
}

func (state tryLockState) String() string {
	return []string{
		trueTryLockState:    "true",
		falseTryLockState:   "false",
		unknownTryLockState: "unknown",
	}[state]
}

func (state tryLockCall) String() string {
	return fmt.Sprintf("%s(%s)", state.state, state.path)
}

func mergeAnd(left []tryLockCall, right []tryLockCall) []tryLockCall {
	result := make([]tryLockCall, 0)

	// Add all left calls.
	for _, leftCall := range left {
		if i := slices.IndexFunc(right, func(rightCall tryLockCall) bool {
			return slices.Equal(leftCall.path, rightCall.path)
		}); i >= 0 {
			if leftCall.state == right[i].state {
				result = append(result, leftCall)
			} else {
				result = append(result, tryLockCall{
					path:    leftCall.path,
					state:   unknownTryLockState,
					isRLock: leftCall.isRLock,
				})
			}
		} else {
			result = append(result, leftCall)
		}
	}

	// Add right calls not in left.
	for _, rightCall := range right {
		if !slices.ContainsFunc(left, func(leftCall tryLockCall) bool {
			return slices.Equal(rightCall.path, leftCall.path)
		}) {
			result = append(result, rightCall)
		}
	}
	return result
}

func mergeOr(left []tryLockCall, right []tryLockCall) []tryLockCall {
	result := make([]tryLockCall, 0)

	// Add all left calls.
	for _, leftCall := range left {
		if i := slices.IndexFunc(right, func(rightCall tryLockCall) bool {
			return slices.Equal(leftCall.path, rightCall.path)
		}); i >= 0 {
			if leftCall.state == right[i].state {
				result = append(result, leftCall)
			} else {
				result = append(result, tryLockCall{
					path:    leftCall.path,
					state:   unknownTryLockState,
					isRLock: leftCall.isRLock,
				})
			}
		} else {
			result = append(result, tryLockCall{
				path:    leftCall.path,
				state:   unknownTryLockState,
				isRLock: leftCall.isRLock,
			})
		}
	}

	// Add right calls not in left.
	for _, rightCall := range right {
		if !slices.ContainsFunc(left, func(leftCall tryLockCall) bool {
			return slices.Equal(rightCall.path, leftCall.path)
		}) {
			result = append(result, tryLockCall{
				path:    rightCall.path,
				state:   unknownTryLockState,
				isRLock: rightCall.isRLock,
			})
		}
	}
	return result
}
