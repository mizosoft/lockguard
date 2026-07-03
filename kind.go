package lockguard

import (
	"go/token"
	"go/types"
)

type lockKind int

const (
	noneLockKind lockKind = iota
	rwLockKind
	normalLockKind
)

func (kind lockKind) String() string {
	return [...]string{
		noneLockKind:   "NoneLock",
		rwLockKind:     "RWLock",
		normalLockKind: "Lock",
	}[kind]
}

// isLocking reports whether funcName is a lock-acquiring method for this lock kind
// (Lock/TryLock for a plain Locker; additionally RLock/TryRLock for an RWLocker).
func (kind lockKind) isLocking(funcName string) bool {
	switch kind {
	case rwLockKind:
		return funcName == "Lock" || funcName == "RLock" ||
				funcName == "TryLock" || funcName == "TryRLock"
	case normalLockKind:
		return funcName == "Lock" || funcName == "TryLock"
	default:
		return false
	}
}

// isUnlocking reports whether funcName is a lock-releasing method for this lock kind
// (Unlock for a plain Locker; Unlock or RUnlock for an RWLocker).
func (kind lockKind) isUnlocking(funcName string) bool {
	switch kind {
	case rwLockKind:
		return funcName == "Unlock" || funcName == "RUnlock"
	case normalLockKind:
		return funcName == "Unlock"
	default:
		return false
	}
}

func lockKindOfObject(lockObj types.Object) lockKind {
	var typ types.Type
	var isFunc bool
	switch lockObj := lockObj.(type) {
	case *types.Func:
		if lockObj.Signature().Results().Len() != 1 {
			return noneLockKind
		}
		typ = lockObj.Signature().Results().At(0).Type()
		isFunc = true
	case *types.Var:
		typ = lockObj.Type()
		isFunc = false
	default:
		return noneLockKind
	}
	return lockKindOf(typ, !isFunc)
}

func lockKindOf(typ types.Type, isPointerReferencable bool) lockKind {
	if isRWLocker(typ, isPointerReferencable) {
		return rwLockKind
	} else if isLocker(typ, isPointerReferencable) {
		return normalLockKind
	} else {
		return noneLockKind
	}
}

var rwLockerInterface *types.Interface
var lockerInterface *types.Interface

func init() {
	nullary := types.NewSignatureType(nil, nil, nil, nil, nil, false) // func()
	rwLockerInterface = types.NewInterfaceType(
		[]*types.Func{
			types.NewFunc(token.NoPos, nil, "Lock", nullary),
			types.NewFunc(token.NoPos, nil, "Unlock", nullary),
			types.NewFunc(token.NoPos, nil, "RLock", nullary),
			types.NewFunc(token.NoPos, nil, "RUnlock", nullary),
		}, nil).Complete()
	lockerInterface = types.NewInterfaceType(
		[]*types.Func{
			types.NewFunc(token.NoPos, nil, "Lock", nullary),
			types.NewFunc(token.NoPos, nil, "Unlock", nullary),
		}, nil).Complete()
}

func isLocker(typ types.Type, isPointerReferencable bool) bool {
	return isOfType(lockerInterface, typ, isPointerReferencable)
}

func isRWLocker(typ types.Type, isPointerReferencable bool) bool {
	return isOfType(rwLockerInterface, typ, isPointerReferencable)
}

func isOfType(target *types.Interface, typ types.Type, isPointerReferencable bool) bool {
	return types.Implements(typ, target) || (isPointerReferencable && types.Implements(types.NewPointer(typ), target))
}
