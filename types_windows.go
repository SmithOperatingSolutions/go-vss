//go:build windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"runtime"
	"time"
	"unicode/utf16"
	"unsafe"
)

// VSS enumeration values used by this package (vss.h).
const (
	vssCtxBackup          = 0x00000000
	vssCtxFileShareBackup = 0x00000010 // VSS_VOLSNAP_ATTR_NO_WRITERS
	vssCtxAppRollback     = 0x00000009 // PERSISTENT | NO_AUTO_RELEASE (writers participate)
	vssCtxNASRollback     = 0x00000019 // PERSISTENT | NO_AUTO_RELEASE | NO_WRITERS
	vssCtxAll             = 0xFFFFFFFF

	vssBtCopy = 5 // VSS_BT_COPY: never triggers writer log truncation

	vssObjectNone     = 1
	vssObjectSnapshot = 3

	vssSSCreated = 12
)

// vssSnapshotProp mirrors VSS_SNAPSHOT_PROP (vss.h) for 64-bit MSVC layout.
// Padding is explicit so the intent survives refactoring; sizes are
// asserted at init.
type vssSnapshotProp struct {
	SnapshotID           guid    // offset 0
	SnapshotSetID        guid    // 16
	SnapshotsCount       int32   // 32
	_                    [4]byte // 36: pad to pointer alignment
	SnapshotDeviceObject *uint16 // 40
	OriginalVolumeName   *uint16 // 48
	OriginatingMachine   *uint16 // 56
	ServiceMachine       *uint16 // 64
	ExposedName          *uint16 // 72
	ExposedPath          *uint16 // 80
	ProviderID           guid    // 88
	SnapshotAttributes   int32   // 104
	_                    [4]byte // 108: pad to 8-byte alignment
	CreationTimestamp    int64   // 112: FILETIME semantics
	Status               int32   // 120
	_                    [4]byte // 124: tail padding
}

// vssObjectProp mirrors VSS_OBJECT_PROP: a type discriminant plus a union
// sized by its largest member (VSS_SNAPSHOT_PROP, 128 bytes).
type vssObjectProp struct {
	Type int32
	_    [4]byte // union aligns to 8 on 64-bit
	Obj  [128]byte
}

// snapshot returns the union reinterpreted as a snapshot property, or nil
// if the discriminant says otherwise — never reinterpret the wrong variant.
func (p *vssObjectProp) snapshot() *vssSnapshotProp {
	if p.Type != vssObjectSnapshot {
		return nil
	}
	return (*vssSnapshotProp)(unsafe.Pointer(&p.Obj[0]))
}

// Struct layout mismatches don't fail loudly — they read garbage pointers.
// Convert that failure mode into a startup panic. Only 64-bit layouts are
// declared; other architectures are refused at runtime by checkSupported.
func init() {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return
	}
	if s := unsafe.Sizeof(vssSnapshotProp{}); s != 128 {
		panic("vss: VSS_SNAPSHOT_PROP layout is wrong for this architecture")
	}
	if s := unsafe.Sizeof(vssObjectProp{}); s != 136 {
		panic("vss: VSS_OBJECT_PROP layout is wrong for this architecture")
	}
	if unsafe.Offsetof(vssSnapshotProp{}.CreationTimestamp) != 112 {
		panic("vss: VSS_SNAPSHOT_PROP.CreationTimestamp misaligned")
	}
}

// freeSnapshotProps releases the five CoTaskMem strings inside a
// VSS_SNAPSHOT_PROP. Must be called after every successful
// GetSnapshotProperties and for every enumerated snapshot property.
func freeSnapshotProps(p *vssSnapshotProp) {
	procs, err := loadProcs()
	if err != nil || p == nil {
		return
	}
	procs.freeSnapshotProps.Call(uintptr(unsafe.Pointer(p)))
}

// utf16PtrToString converts a NUL-terminated wide string with a bounded
// scan: a corrupt pointer must not walk the heap.
func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	const maxChars = 32 * 1024
	buf := unsafe.Slice(p, maxChars)
	for i, c := range buf {
		if c == 0 {
			return string(utf16.Decode(buf[:i]))
		}
	}
	return string(utf16.Decode(buf)) // truncated; treat as suspect
}

// filetimeToTime converts a VSS_TIMESTAMP (FILETIME: 100ns ticks since
// 1601-01-01 UTC) to time.Time.
func filetimeToTime(ft int64) time.Time {
	const epochDelta = 116444736000000000
	return time.Unix(0, (ft-epochDelta)*100).UTC()
}

// snapshotFromProp copies a native property struct into a Go Snapshot.
// The caller still owns (and must free) the native struct.
func snapshotFromProp(p *vssSnapshotProp) Snapshot {
	return Snapshot{
		ID:           p.SnapshotID.String(),
		SetID:        p.SnapshotSetID.String(),
		Volume:       utf16PtrToString(p.OriginalVolumeName),
		DeviceObject: utf16PtrToString(p.SnapshotDeviceObject),
		CreatedAt:    filetimeToTime(p.CreationTimestamp),
		Attributes:   uint32(p.SnapshotAttributes),
		State:        SnapshotState(p.Status),
	}
}
