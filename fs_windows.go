//go:build windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"fmt"
	"io/fs"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// openInSnapshot opens dev + `\` + rel with backup semantics. rel must
// already be validated. An empty rel opens the filesystem root (the
// trailing backslash is what selects the filesystem over the raw device).
func openInSnapshot(dev, rel, display string) (*os.File, error) {
	full := dev + `\` + rel
	p, err := utf16Ptr(full)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: display, Err: err}
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ,
		// The snapshot is read-only; denying share modes gains nothing
		// and risks spurious sharing violations.
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		// BACKUP_SEMANTICS: required for directories, and lets
		// SeBackupPrivilege bypass DACLs. OPEN_REPARSE_POINT: never follow
		// symlinks/junctions — a link inside the snapshot can point at the
		// LIVE volume, silently escaping the point-in-time view.
		windows.FILE_FLAG_BACKUP_SEMANTICS|
			windows.FILE_FLAG_OPEN_REPARSE_POINT|
			windows.FILE_FLAG_SEQUENTIAL_SCAN,
		0,
	)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: display, Err: err}
	}
	return os.NewFile(uintptr(h), display), nil
}

// Open opens a file or directory inside the snapshot by volume-relative
// path (e.g. `Users\bob\file.txt`; forward slashes accepted; "" or "."
// opens the root). Paths are validated and rejected — not repaired — on
// `..`, drive letters, leading separators, or NUL. Reparse points are
// opened, never followed.
func (s *Snapshot) Open(rel string) (*os.File, error) {
	if s.DeviceObject == "" {
		return nil, fmt.Errorf("vss: snapshot has no device object")
	}
	norm, err := validateVolumeRelativePath(rel)
	if err != nil {
		return nil, err
	}
	return openInSnapshot(s.DeviceObject, norm, rel)
}

// OpenRaw opens the shadow copy as a raw block device (no trailing
// backslash on the device object selects the volume, not the filesystem).
// Intended for image/block-level backup. Reads are buffered; offsets need
// not be sector-aligned.
func (s *Snapshot) OpenRaw() (*os.File, error) {
	if s.DeviceObject == "" {
		return nil, fmt.Errorf("vss: snapshot has no device object")
	}
	p, err := utf16Ptr(s.DeviceObject)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_SEQUENTIAL_SCAN,
		0,
	)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: s.DeviceObject, Err: err}
	}
	return os.NewFile(uintptr(h), s.DeviceObject), nil
}

// FS returns a read-only fs.FS rooted at the snapshot's filesystem root.
// It follows io/fs conventions (forward-slash paths, "." for the root) and
// works with fs.WalkDir, fs.ReadFile, etc. Reparse points are reported,
// not followed.
func (s *Snapshot) FS() fs.FS {
	return &snapshotFS{dev: s.DeviceObject}
}

type snapshotFS struct {
	dev string
}

func (f *snapshotFS) Open(name string) (fs.File, error) {
	if f.dev == "" {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	// fs.ValidPath rejects "..", absolute paths, and empty names — but it
	// permits backslashes and colons inside components, which would let a
	// caller smuggle separators or stream syntax past us. Reject those too.
	if !fs.ValidPath(name) || strings.ContainsAny(name, `\:`) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	rel := ""
	if name != "." {
		rel = strings.ReplaceAll(name, "/", `\`)
	}
	file, err := openInSnapshot(f.dev, rel, name)
	if err != nil {
		return nil, err
	}
	return file, nil
}
