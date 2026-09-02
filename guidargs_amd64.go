//go:build windows && amd64

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"syscall"
	"unsafe"
)

// On amd64 (Microsoft x64 ABI), a 16-byte struct argument is passed by
// reference: the caller allocates a copy and passes its address. The `g`
// parameters below are those copies; their addresses are taken inside the
// SyscallN expression per unsafe.Pointer rule (4).

func (v *backupComponents) addToSnapshotSetCall(volp *uint16, g guid, out *guid) uintptr {
	hr, _, _ := syscall.SyscallN(v.vtbl.addToSnapshotSet,
		uintptr(unsafe.Pointer(v)),
		uintptr(unsafe.Pointer(volp)),
		uintptr(unsafe.Pointer(&g)),
		uintptr(unsafe.Pointer(out)),
	)
	return hr
}

func (v *backupComponents) isVolumeSupportedCall(g guid, volp *uint16, out *int32) uintptr {
	hr, _, _ := syscall.SyscallN(v.vtbl.isVolumeSupported,
		uintptr(unsafe.Pointer(v)),
		uintptr(unsafe.Pointer(&g)),
		uintptr(unsafe.Pointer(volp)),
		uintptr(unsafe.Pointer(out)),
	)
	return hr
}

func (v *backupComponents) getSnapshotPropertiesCall(g guid, prop *vssSnapshotProp) uintptr {
	hr, _, _ := syscall.SyscallN(v.vtbl.getSnapshotProperties,
		uintptr(unsafe.Pointer(v)),
		uintptr(unsafe.Pointer(&g)),
		uintptr(unsafe.Pointer(prop)),
	)
	return hr
}

func (v *backupComponents) deleteSnapshotsCall(g guid, objType int32, force uintptr, deleted *int32, nonDeleted *guid) uintptr {
	hr, _, _ := syscall.SyscallN(v.vtbl.deleteSnapshots,
		uintptr(unsafe.Pointer(v)),
		uintptr(unsafe.Pointer(&g)),
		uintptr(uint32(objType)),
		force,
		uintptr(unsafe.Pointer(deleted)),
		uintptr(unsafe.Pointer(nonDeleted)),
	)
	return hr
}

func (v *backupComponents) queryCall(g guid, queriedType, returnedType int32, out **enumObject) uintptr {
	hr, _, _ := syscall.SyscallN(v.vtbl.query,
		uintptr(unsafe.Pointer(v)),
		uintptr(unsafe.Pointer(&g)),
		uintptr(uint32(queriedType)),
		uintptr(uint32(returnedType)),
		uintptr(unsafe.Pointer(out)),
	)
	return hr
}
