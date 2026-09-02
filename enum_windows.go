//go:build windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"syscall"
	"unsafe"
)

// enumObject wraps IVssEnumObject (vss.h).
type enumObject struct {
	vtbl *enumVtbl
}

type enumVtbl struct {
	queryInterface uintptr // 0
	addRef         uintptr // 1
	release        uintptr // 2
	next           uintptr // 3
	skip           uintptr // 4
	reset          uintptr // 5
	clone          uintptr // 6
}

func (e *enumObject) Release() {
	syscall.SyscallN(e.vtbl.release, uintptr(unsafe.Pointer(e)))
}

// Next fills props and returns how many were fetched and whether the
// enumeration is exhausted (S_FALSE). Each fetched property owns CoTaskMem
// strings the caller must free via freeSnapshotProps on the snapshot
// variant.
func (e *enumObject) Next(props []vssObjectProp) (n uint32, done bool, err error) {
	if len(props) == 0 {
		return 0, false, nil
	}
	hr, _, _ := syscall.SyscallN(e.vtbl.next,
		uintptr(unsafe.Pointer(e)),
		uintptr(uint32(len(props))),
		uintptr(unsafe.Pointer(&props[0])),
		uintptr(unsafe.Pointer(&n)),
	)
	h := uint32(hr)
	if hrFailed(h) {
		return 0, false, &Error{Op: "IVssEnumObject::Next", HRESULT: h}
	}
	return n, h == hrSFalse, nil
}
