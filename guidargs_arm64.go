//go:build windows && arm64

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"syscall"
	"unsafe"
)

// On Windows ARM64 (AAPCS64), composite types of 16 bytes or less are
// passed by value in general-purpose registers: a GUID occupies two
// X-registers as its raw 8-byte halves. Passing a pointer here — the amd64
// shape — silently corrupts every argument that follows.
//
// This shape is validated at runtime by createSet: the device object
// returned by GetSnapshotProperties must parse as a GLOBALROOT path, which
// an ABI mismatch cannot produce.

func guidWords(g *guid) (uintptr, uintptr) {
	w := (*[2]uint64)(unsafe.Pointer(g))
	return uintptr(w[0]), uintptr(w[1])
}

func (v *backupComponents) addToSnapshotSetCall(volp *uint16, g guid, out *guid) uintptr {
	lo, hi := guidWords(&g)
	hr, _, _ := syscall.SyscallN(v.vtbl.addToSnapshotSet,
		uintptr(unsafe.Pointer(v)),
		uintptr(unsafe.Pointer(volp)),
		lo, hi,
		uintptr(unsafe.Pointer(out)),
	)
	return hr
}

func (v *backupComponents) isVolumeSupportedCall(g guid, volp *uint16, out *int32) uintptr {
	lo, hi := guidWords(&g)
	hr, _, _ := syscall.SyscallN(v.vtbl.isVolumeSupported,
		uintptr(unsafe.Pointer(v)),
		lo, hi,
		uintptr(unsafe.Pointer(volp)),
		uintptr(unsafe.Pointer(out)),
	)
	return hr
}

func (v *backupComponents) getSnapshotPropertiesCall(g guid, prop *vssSnapshotProp) uintptr {
	lo, hi := guidWords(&g)
	hr, _, _ := syscall.SyscallN(v.vtbl.getSnapshotProperties,
		uintptr(unsafe.Pointer(v)),
		lo, hi,
		uintptr(unsafe.Pointer(prop)),
	)
	return hr
}

func (v *backupComponents) deleteSnapshotsCall(g guid, objType int32, force uintptr, deleted *int32, nonDeleted *guid) uintptr {
	lo, hi := guidWords(&g)
	hr, _, _ := syscall.SyscallN(v.vtbl.deleteSnapshots,
		uintptr(unsafe.Pointer(v)),
		lo, hi,
		uintptr(uint32(objType)),
		force,
		uintptr(unsafe.Pointer(deleted)),
		uintptr(unsafe.Pointer(nonDeleted)),
	)
	return hr
}

func (v *backupComponents) queryCall(g guid, queriedType, returnedType int32, out **enumObject) uintptr {
	lo, hi := guidWords(&g)
	hr, _, _ := syscall.SyscallN(v.vtbl.query,
		uintptr(unsafe.Pointer(v)),
		lo, hi,
		uintptr(uint32(queriedType)),
		uintptr(uint32(returnedType)),
		uintptr(unsafe.Pointer(out)),
	)
	return hr
}
