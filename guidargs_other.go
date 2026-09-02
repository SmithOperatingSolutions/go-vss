//go:build windows && !amd64 && !arm64

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

// Stubs so 32-bit Windows builds compile; checkSupported refuses these
// architectures before any of this can be reached. 386 would additionally
// need different struct padding (MSVC 8-byte int64 alignment vs Go's 4)
// and a 4-dword GUID call shape — deliberately unimplemented.

const hrEFail = 0x80004005

func (v *backupComponents) addToSnapshotSetCall(volp *uint16, g guid, out *guid) uintptr {
	return hrEFail
}

func (v *backupComponents) isVolumeSupportedCall(g guid, volp *uint16, out *int32) uintptr {
	return hrEFail
}

func (v *backupComponents) getSnapshotPropertiesCall(g guid, prop *vssSnapshotProp) uintptr {
	return hrEFail
}

func (v *backupComponents) deleteSnapshotsCall(g guid, objType int32, force uintptr, deleted *int32, nonDeleted *guid) uintptr {
	return hrEFail
}

func (v *backupComponents) queryCall(g guid, queriedType, returnedType int32, out **enumObject) uintptr {
	return hrEFail
}
