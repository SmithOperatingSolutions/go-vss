// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import "errors"

var (
	errNonMonotonicVCN = errors.New("vss: retrieval pointers reply has non-monotonic VCNs")
	errBadClusterSize  = errors.New("vss: invalid cluster size")
)

// Extent is a contiguous run of a file's data on the volume. All fields
// are byte offsets/lengths. VolumeOffset is relative to the start of the
// volume — exactly the address space of Snapshot.OpenRaw.
type Extent struct {
	// FileOffset is the byte offset within the file's logical data.
	FileOffset int64
	// VolumeOffset is the byte offset on the raw volume.
	VolumeOffset int64
	// Length is the number of bytes in this run. Cluster-granular: the
	// last run of a file may extend past its logical size into slack;
	// read the full extent, then trim to the file size.
	Length int64
}

// vcnRun is one cluster run from RETRIEVAL_POINTERS_BUFFER: the file's
// clusters [prevVCN, NextVCN) live at volume cluster LCN (or nowhere,
// when LCN is negative — a sparse hole).
type vcnRun struct {
	NextVCN int64
	LCN     int64
}

// appendRuns converts cluster runs into byte extents, starting from
// prevVCN, and returns the updated slice and the VCN to continue from.
// Sparse runs are skipped; non-monotonic VCNs are rejected (fail closed —
// a malformed reply must not fabricate extents). Pure function so the
// offset math is testable off-Windows.
func appendRuns(out []Extent, prevVCN int64, runs []vcnRun, clusterSize int64) ([]Extent, int64, error) {
	if clusterSize <= 0 {
		return out, prevVCN, errBadClusterSize
	}
	for _, r := range runs {
		if r.NextVCN <= prevVCN {
			return out, prevVCN, errNonMonotonicVCN
		}
		if r.LCN >= 0 {
			out = append(out, Extent{
				FileOffset:   prevVCN * clusterSize,
				VolumeOffset: r.LCN * clusterSize,
				Length:       (r.NextVCN - prevVCN) * clusterSize,
			})
		}
		prevVCN = r.NextVCN
	}
	return out, prevVCN, nil
}

// VolumeGeometry reports NTFS layout facts for the snapshot volume, from
// FSCTL_GET_NTFS_VOLUME_DATA. All offsets and sizes are in bytes. It is
// read from the shadow device (unlike the USN FSCTLs, which NTFS only
// serves from the live volume).
//
// Returns ErrUnsupported off Windows / 32-bit / WOW64, and an error on a
// non-NTFS volume (the FSCTL is NTFS-only).
type VolumeGeometry struct {
	BytesPerSector  int64
	BytesPerCluster int64
	TotalClusters   int64
	FreeClusters    int64
	// MFTStart is the byte offset of the $MFT on the volume.
	MFTStart int64
	// MFTRecordSize is the size in bytes of one MFT file-record segment.
	MFTRecordSize int64
}
