//go:build windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"strings"
	"testing"
	"time"
	"unsafe"
)

// TestStructLayout duplicates the init assertions as visible test results.
func TestStructLayout(t *testing.T) {
	if s := unsafe.Sizeof(vssSnapshotProp{}); s != 128 {
		t.Errorf("sizeof(VSS_SNAPSHOT_PROP) = %d, want 128", s)
	}
	if s := unsafe.Sizeof(vssObjectProp{}); s != 136 {
		t.Errorf("sizeof(VSS_OBJECT_PROP) = %d, want 136", s)
	}
	offsets := map[string]uintptr{
		"SnapshotDeviceObject": unsafe.Offsetof(vssSnapshotProp{}.SnapshotDeviceObject),
		"CreationTimestamp":    unsafe.Offsetof(vssSnapshotProp{}.CreationTimestamp),
		"Status":               unsafe.Offsetof(vssSnapshotProp{}.Status),
	}
	want := map[string]uintptr{
		"SnapshotDeviceObject": 40,
		"CreationTimestamp":    112,
		"Status":               120,
	}
	for name, off := range offsets {
		if off != want[name] {
			t.Errorf("offsetof(%s) = %d, want %d", name, off, want[name])
		}
	}
}

// TestExportsResolve confirms vssapi.dll exports resolve on this Windows
// build/architecture (no elevation needed). This is the cheapest cross-check
// that the decorated names in proc_windows.go are right — including on the
// arm64 CI runner.
func TestExportsResolve(t *testing.T) {
	if err := checkSupported(); err != nil {
		t.Skipf("architecture unsupported: %v", err)
	}
	procs, err := loadProcs()
	if err != nil {
		t.Fatalf("resolving vssapi.dll exports: %v", err)
	}
	if procs.createBackupComponents == nil || procs.freeSnapshotProps == nil {
		t.Fatal("nil procs after successful load")
	}
}

func TestFiletimeToTime(t *testing.T) {
	// 2020-01-01T00:00:00Z as FILETIME.
	const ft = 132223104000000000
	got := filetimeToTime(ft)
	want := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("filetimeToTime = %v, want %v", got, want)
	}
}

func TestExpandEnv(t *testing.T) {
	got := expandEnv(`%SystemRoot%\Temp`)
	if got == `%SystemRoot%\Temp` || !strings.Contains(strings.ToUpper(got), `\WINDOWS\TEMP`) {
		t.Errorf("expandEnv(%%SystemRoot%%\\Temp) = %q", got)
	}
	// No-percent fast path and unknown-variable behavior must not mangle.
	if got := expandEnv(`C:\plain\path`); got != `C:\plain\path` {
		t.Errorf("expandEnv(plain) = %q", got)
	}
	if got := expandEnv(`%NoSuchVssVar123%\x`); got != `%NoSuchVssVar123%\x` {
		t.Errorf("expandEnv(unknown var) = %q, want unchanged", got)
	}
}

// Zero-value and misuse paths for Volume must error, not panic — no
// elevation required.
func TestVolumeZeroValueAndBadPaths(t *testing.T) {
	var v Volume
	if _, err := v.VolumeGeometry(); err == nil {
		t.Error("zero Volume VolumeGeometry should error")
	}
	if _, err := v.FileExtents("x"); err == nil {
		t.Error("zero Volume FileExtents should error")
	}
	if _, err := v.Open("x"); err == nil {
		t.Error("zero Volume Open should error")
	}
	if err := checkSupported(); err != nil {
		t.Skipf("unsupported: %v", err)
	}
	if _, err := OpenVolume(`Q:\no\such\volume\hopefully`); err == nil {
		t.Error("OpenVolume on a nonexistent volume should error")
	}
}

func TestVssObjectPropUnionDiscriminant(t *testing.T) {
	p := vssObjectProp{Type: 4 /* VSS_OBJECT_PROVIDER */}
	if p.snapshot() != nil {
		t.Error("snapshot() must return nil for non-snapshot variants")
	}
	p.Type = vssObjectSnapshot
	if p.snapshot() == nil {
		t.Error("snapshot() must return the union for snapshot variants")
	}
}
