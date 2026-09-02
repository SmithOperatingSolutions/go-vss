// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

// Volume is a live NTFS volume opened for the same read primitives as
// Snapshot — file opens, geometry, and physical extents — without a
// shadow copy. It exists for engine paths that deliberately operate on
// the live filesystem (no-VSS capture modes, mapping a backup
// repository's own clusters for self-exclusion).
//
// Consistency caveat, by design: the live volume changes underneath you.
// Extents and content read here are only as stable as the caller's own
// quiescence guarantees — a Snapshot is the consistent path. There is
// deliberately no OpenRaw on Volume: bulk raw reads of a live volume
// race concurrent writes; take a snapshot for that.
type Volume struct {
	// MountPoint is the mount path the volume was resolved through
	// (e.g. `C:\`), useful for building ExcludeRule paths. Equal to
	// VolumeName when the volume was opened by GUID path.
	MountPoint string
	// VolumeName is the canonical `\\?\Volume{GUID}\` name. All file
	// opens and FSCTLs address the volume through this form, which —
	// like a shadow device object — serves both roles: without the
	// trailing backslash it opens the raw volume, with `\rel` appended
	// it opens files.
	VolumeName string
}

// DiskExtent locates a slice of a volume on a physical disk
// (`\\.\PhysicalDrive<DiskNumber>`). All offsets and lengths are bytes
// from the start of the disk.
type DiskExtent struct {
	DiskNumber     uint32
	StartingOffset int64
	Length         int64
}
