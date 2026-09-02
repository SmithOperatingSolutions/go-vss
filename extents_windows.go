//go:build windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	fsctlGetNTFSVolumeData    = 0x00090064
	fsctlGetRetrievalPointers = 0x00090073
)

// ntfsVolumeDataBuffer mirrors NTFS_VOLUME_DATA_BUFFER (winioctl.h); the
// extended tail is not requested.
type ntfsVolumeDataBuffer struct {
	VolumeSerialNumber           int64
	NumberSectors                int64
	TotalClusters                int64
	FreeClusters                 int64
	TotalReserved                int64
	BytesPerSector               uint32
	BytesPerCluster              uint32
	BytesPerFileRecordSegment    uint32
	ClustersPerFileRecordSegment uint32
	MftValidDataLength           int64
	MftStartLcn                  int64
	Mft2StartLcn                 int64
	MftZoneStart                 int64
	MftZoneEnd                   int64
}

func init() {
	if unsafe.Sizeof(ntfsVolumeDataBuffer{}) != 96 {
		panic("vss: NTFS_VOLUME_DATA_BUFFER layout is wrong")
	}
}

// geomCache memoizes VolumeGeometry per snapshot: the geometry is
// immutable for a snapshot's lifetime and FileExtents needs it per file.
// Keyed by the shadow copy ID, which is globally unique — device object
// names (HarddiskVolumeShadowCopyN) are recycled by Windows after
// deletion, and a name-keyed cache could serve one volume's geometry for
// another. (A package map rather than a field keeps Snapshot free of
// lock-bearing state, which callers copy.)
var geomCache sync.Map // snapshot ID string -> VolumeGeometry

// VolumeGeometry reports NTFS layout facts for the snapshot volume.
// Results are cached per snapshot; concurrent use is safe.
func (s *Snapshot) VolumeGeometry() (VolumeGeometry, error) {
	if s.DeviceObject == "" {
		return VolumeGeometry{}, fmt.Errorf("vss: snapshot has no device object")
	}
	cacheKey := s.ID
	if cacheKey == "" {
		cacheKey = s.DeviceObject // hand-constructed Snapshot; best effort
	}
	return cachedVolumeGeometry(cacheKey, s.DeviceObject)
}

// cachedVolumeGeometry opens the raw volume at devBase (no trailing
// backslash — shadow device object or `\\?\Volume{GUID}`) and queries or
// returns cached geometry.
func cachedVolumeGeometry(cacheKey, devBase string) (VolumeGeometry, error) {
	if g, ok := geomCache.Load(cacheKey); ok {
		return g.(VolumeGeometry), nil
	}
	h, err := openVolumeByPath(devBase)
	if err != nil {
		return VolumeGeometry{}, err
	}
	defer windows.CloseHandle(h)
	g, err := volumeGeometryFromHandle(h)
	if err != nil {
		return VolumeGeometry{}, err
	}
	geomCache.Store(cacheKey, g)
	return g, nil
}

func volumeGeometryFromHandle(h windows.Handle) (VolumeGeometry, error) {
	var b ntfsVolumeDataBuffer
	var ret uint32
	if err := windows.DeviceIoControl(h, fsctlGetNTFSVolumeData,
		nil, 0,
		(*byte)(unsafe.Pointer(&b)), uint32(unsafe.Sizeof(b)), &ret, nil); err != nil {
		// ERROR_INVALID_FUNCTION on non-NTFS volumes.
		return VolumeGeometry{}, fmt.Errorf("vss: FSCTL_GET_NTFS_VOLUME_DATA (non-NTFS volume?): %w", err)
	}
	if b.BytesPerCluster == 0 || b.BytesPerSector == 0 {
		return VolumeGeometry{}, fmt.Errorf("vss: FSCTL_GET_NTFS_VOLUME_DATA returned zero geometry")
	}
	return VolumeGeometry{
		BytesPerSector:  int64(b.BytesPerSector),
		BytesPerCluster: int64(b.BytesPerCluster),
		TotalClusters:   b.TotalClusters,
		FreeClusters:    b.FreeClusters,
		MFTStart:        b.MftStartLcn * int64(b.BytesPerCluster),
		MFTRecordSize:   int64(b.BytesPerFileRecordSegment),
	}, nil
}

// STARTING_VCN_INPUT_BUFFER / RETRIEVAL_POINTERS_BUFFER (winioctl.h).
type startingVCNInput struct {
	VCN int64
}

type retrievalPointersHeader struct {
	ExtentCount int32
	_           [4]byte // pad to 8-byte-align StartingVCN
	StartingVCN int64
	// followed inline by [ExtentCount]retrievalExtent
}

type retrievalExtent struct {
	NextVCN int64
	LCN     int64 // -1 == sparse/unallocated
}

func init() {
	if unsafe.Sizeof(retrievalPointersHeader{}) != 16 || unsafe.Sizeof(retrievalExtent{}) != 16 {
		panic("vss: RETRIEVAL_POINTERS_BUFFER layout is wrong")
	}
}

// FileExtents returns the physical extents of rel's default data stream
// within the snapshot, for reading the file's content by offset from the
// raw volume (Snapshot.OpenRaw). rel has the same validation and
// semantics as Open; reparse points are opened, never followed.
//
// The result is empty (nil error) for resident files — small files whose
// data lives inside the MFT record — and for zero-length files; read
// those with Open instead. Sparse (unallocated) runs are omitted: gaps
// between FileOffsets are holes that read as zeros. Extents are in
// ascending FileOffset order and cluster-granular — read the full extent
// from the raw device (offsets and lengths stay sector-aligned that way),
// then trim to the file's logical size.
func (s *Snapshot) FileExtents(rel string) ([]Extent, error) {
	if s.DeviceObject == "" {
		return nil, fmt.Errorf("vss: snapshot has no device object")
	}
	geom, err := s.VolumeGeometry()
	if err != nil {
		return nil, err
	}
	return fileExtentsUnder(s.DeviceObject, rel, geom.BytesPerCluster)
}

// fileExtentsUnder opens devBase\rel (validated) and maps its cluster
// runs; shared by Snapshot (shadow device base) and Volume (live volume
// GUID base).
func fileExtentsUnder(devBase, rel string, cs int64) ([]Extent, error) {
	norm, err := validateVolumeRelativePath(rel)
	if err != nil {
		return nil, err
	}
	f, err := openInSnapshot(devBase, norm, rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := windows.Handle(f.Fd())

	// uint64 backing keeps the METHOD_NEITHER output buffer 8-aligned.
	const maxExtents = 512
	buf := make([]uint64, (16+maxExtents*16)/8)
	out := []Extent(nil)

	in := startingVCNInput{VCN: 0}
	for {
		var ret uint32
		ioErr := windows.DeviceIoControl(h, fsctlGetRetrievalPointers,
			(*byte)(unsafe.Pointer(&in)), uint32(unsafe.Sizeof(in)),
			(*byte)(unsafe.Pointer(&buf[0])), uint32(len(buf)*8), &ret, nil)

		// Resident (in-MFT) and zero-length files have no cluster runs.
		if ioErr == windows.ERROR_HANDLE_EOF {
			return out, nil
		}
		// Parse only on success or ERROR_MORE_DATA (a valid partial
		// buffer); parsing after any other failure would fabricate
		// extents from stale memory.
		if ioErr != nil && ioErr != windows.ERROR_MORE_DATA {
			return nil, fmt.Errorf("vss: FSCTL_GET_RETRIEVAL_POINTERS(%q): %w", rel, ioErr)
		}
		if ret < 16 {
			return nil, fmt.Errorf("vss: FSCTL_GET_RETRIEVAL_POINTERS(%q): short reply (%d bytes)", rel, ret)
		}

		hdr := (*retrievalPointersHeader)(unsafe.Pointer(&buf[0]))
		n := int(hdr.ExtentCount)
		if n < 0 || n > (int(ret)-16)/16 {
			return nil, fmt.Errorf("vss: FSCTL_GET_RETRIEVAL_POINTERS(%q): extent count %d exceeds reply size %d", rel, n, ret)
		}
		if n == 0 && ioErr == windows.ERROR_MORE_DATA {
			return nil, fmt.Errorf("vss: FSCTL_GET_RETRIEVAL_POINTERS(%q): no forward progress", rel)
		}

		runs := make([]vcnRun, n)
		for i := 0; i < n; i++ {
			e := (*retrievalExtent)(unsafe.Add(unsafe.Pointer(&buf[0]), 16+i*16))
			runs[i] = vcnRun{NextVCN: e.NextVCN, LCN: e.LCN}
		}
		var prevVCN int64
		out, prevVCN, err = appendRuns(out, hdr.StartingVCN, runs, cs)
		if err != nil {
			return nil, fmt.Errorf("vss: FSCTL_GET_RETRIEVAL_POINTERS(%q): %w", rel, err)
		}

		if ioErr == nil {
			return out, nil
		}
		in.VCN = prevVCN // ERROR_MORE_DATA: resume from the last NextVCN
	}
}
