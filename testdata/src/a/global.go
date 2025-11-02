package a

import (
	"b"
	"sync"
)

var globalMut sync.Mutex

//lockguard:protected_by globalMut
var globalI int

func accessGlobalI() {
	globalI++ // want `globalMut is not held while accessing globalI`

	globalMut.Lock()
	globalI++
	globalMut.Unlock()
}

//lockguard:protected_by globalMut
func globalFunc() {
	globalI++
}

func accessGlobalFunc() {
	globalFunc() // want `globalMut is not held while accessing globalFunc`

	globalMut.Lock()
	globalFunc()
	globalMut.Unlock()
}

//lockguard:protected_by globalMut
//lockguard:protected_by b.GlobalMut
var globalJ int

//lockguard:protected_by globalMut
//lockguard:protected_by b.GlobalMut
func globalFunc2() {
	globalJ++
	globalI++
}

func globalWith2Locks() {
	globalJ++     // want `globalMut, GlobalMut is not held while accessing globalJ`
	globalFunc2() // want `globalMut, GlobalMut is not held while accessing globalFunc2`

	globalMut.Lock()
	globalJ++     // want `GlobalMut is not held while accessing globalJ`
	globalFunc2() // want `GlobalMut is not held while accessing globalFunc2`
	globalMut.Unlock()

	b.GlobalMut.Lock()
	globalJ++     // want `globalMut is not held while accessing globalJ`
	globalFunc2() // want `globalMut is not held while accessing globalFunc2`
	b.GlobalMut.Unlock()

}
