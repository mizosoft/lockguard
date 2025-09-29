package lockgaurd

import (
	"go/importer"
	"go/types"
)

var lockerType *types.Interface

func init() {
	// Load sync package to get the Locker interface
	imp := importer.Default()
	syncPkg, err := imp.Import("sync")
	if err != nil {
		panic(err)
	}

	obj := syncPkg.Scope().Lookup("Locker")
	if typeName, ok := obj.(*types.TypeName); ok {
		if named, ok := typeName.Type().(*types.Named); ok {
			if iface, ok := named.Underlying().(*types.Interface); ok {
				lockerType = iface
			}
		}
	}

	if lockerType == nil {
		panic("unable to find sync.Locker")
	}
}

func isLocker(typ types.Type, considerPointer bool) bool {
	return types.Implements(typ, lockerType) || (considerPointer && types.Implements(types.NewPointer(typ), lockerType))
}
