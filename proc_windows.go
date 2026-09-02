//go:build windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// vssapi.dll is resolved with system32-only search semantics
// (NewLazySystemDLL); a backup agent that loads DLLs from its working
// directory is a local-privilege-escalation bug.
var modVssapi = windows.NewLazySystemDLL("vssapi.dll")

// findProc probes an ordered candidate list and fails closed if none
// resolve. vssapi.dll exports these entry points both under MSVC-decorated
// C++ names (what shipping builds carry) and documented `...Internal`
// aliases; which set exists varies by Windows build.
func findProc(candidates ...string) (*windows.LazyProc, error) {
	for _, name := range candidates {
		p := modVssapi.NewProc(name)
		if err := p.Find(); err == nil {
			return p, nil
		}
	}
	return nil, fmt.Errorf("vssapi.dll: none of %v resolved", candidates)
}

var loadProcs = sync.OnceValues(func() (struct {
	createBackupComponents *windows.LazyProc
	freeSnapshotProps      *windows.LazyProc
}, error) {
	var procs struct {
		createBackupComponents *windows.LazyProc
		freeSnapshotProps      *windows.LazyProc
	}
	switch runtime.GOARCH {
	case "amd64", "arm64":
	default:
		return procs, ErrUnsupported
	}
	var err error
	procs.createBackupComponents, err = findProc(
		"?CreateVssBackupComponents@@YAJPEAPEAVIVssBackupComponents@@@Z",
		"CreateVssBackupComponentsInternal",
	)
	if err != nil {
		return procs, err
	}
	procs.freeSnapshotProps, err = findProc(
		"?VssFreeSnapshotProperties@@YAXPEAU_VSS_SNAPSHOT_PROP@@@Z",
		"VssFreeSnapshotPropertiesInternal",
	)
	return procs, err
})

// checkSupported refuses unsupported configurations up front: non-64-bit
// builds, and any binary running under WOW64/emulation. Microsoft does not
// support mismatched-architecture VSS requesters — 64-bit writers misbehave
// or become invisible, which corrupts backups silently.
func checkSupported() error {
	switch runtime.GOARCH {
	case "amd64", "arm64":
	default:
		return ErrUnsupported
	}
	var processMachine, nativeMachine uint16
	err := windows.IsWow64Process2(windows.CurrentProcess(), &processMachine, &nativeMachine)
	// IMAGE_FILE_MACHINE_UNKNOWN (0) means "not running under WOW64".
	if err == nil && processMachine != 0 {
		return fmt.Errorf("vss: this binary runs under WOW64/emulation on a different native architecture; build natively: %w", ErrUnsupported)
	}
	return nil
}

// enableBackupPrivilege enables SeBackupPrivilege on the process token so
// snapshot reads can bypass DACLs. Fails closed: AdjustTokenPrivileges
// reports partial failure via GetLastError while still returning success,
// and proceeding without the privilege yields backups with silently
// missing files.
func enableBackupPrivilege() error {
	return enablePrivilege("SeBackupPrivilege")
}

var (
	modAdvapi32                  = windows.NewLazySystemDLL("advapi32.dll")
	procAdjustTokenPrivilegesRaw = modAdvapi32.NewProc("AdjustTokenPrivileges")
)

func enablePrivilege(name string) error {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(),
		windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		return fmt.Errorf("vss: OpenProcessToken: %w", err)
	}
	defer tok.Close()

	namep, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, namep, &luid); err != nil {
		return fmt.Errorf("vss: LookupPrivilegeValue(%s): %w", name, err)
	}
	tp := windows.Tokenprivileges{
		PrivilegeCount: 1,
		Privileges: [1]windows.LUIDAndAttributes{
			{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED},
		},
	}
	// Called via the raw proc (not x/sys's wrapper) because the wrapper
	// discards GetLastError on success, and ERROR_NOT_ALL_ASSIGNED is
	// reported that way with a success return.
	r1, _, lastErr := procAdjustTokenPrivilegesRaw.Call(
		uintptr(tok), 0,
		uintptr(unsafe.Pointer(&tp)), 0, 0, 0,
	)
	if r1 == 0 {
		return fmt.Errorf("vss: AdjustTokenPrivileges(%s): %w", name, lastErr)
	}
	if errno, ok := lastErr.(windows.Errno); ok && errno == windows.ERROR_NOT_ALL_ASSIGNED {
		return fmt.Errorf("vss: privilege %s is not held by this token; run elevated", name)
	}
	return nil
}
