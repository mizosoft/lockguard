package a

import (
	"b"
	"sync"
)

var globalMut sync.Mutex

//lockguard:protected_by globalMut
var globalI int

func accessGlobalI() {
	globalI++ // want `writing 'globalI' requires holding 'globalMut'`

	globalMut.Lock()
	globalI++
	globalMut.Unlock()
}

//lockguard:protected_by globalMut
func globalFunc() {
	globalI++
}

func accessGlobalFunc() {
	globalFunc() // want `reading 'globalFunc' requires holding 'globalMut'`

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
	globalJ++     // want `writing 'globalJ' requires holding 'globalMut' and 'b\.GlobalMut'`
	globalFunc2() // want `reading 'globalFunc2' requires holding 'globalMut' and 'b\.GlobalMut'`

	globalMut.Lock()
	globalJ++     // want `writing 'globalJ' requires holding 'b\.GlobalMut'`
	globalFunc2() // want `reading 'globalFunc2' requires holding 'b\.GlobalMut'`
	globalMut.Unlock()

	b.GlobalMut.Lock()
	globalJ++     // want `writing 'globalJ' requires holding 'globalMut'`
	globalFunc2() // want `reading 'globalFunc2' requires holding 'globalMut'`
	b.GlobalMut.Unlock()
}
