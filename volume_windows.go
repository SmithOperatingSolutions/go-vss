//go:build windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// OpenVolume resolves a live volume for extent/geometry/file queries.
// path may be a drive root (`C:` or `C:\`), a mount point, any path on
// the volume, or a `\\?\Volume{GUID}\` name. Requires the same platform
// support as snapshots (native 64-bit Windows); elevation is needed for
// the geometry FSCTL and DACL-bypassing reads.
func OpenVolume(path string) (*Volume, error) {
	if err := checkSupported(); err != nil {
		return nil, err
	}
	vol, mount, err := resolveVolumeAndMount(path)
	if err != nil {
		return nil, err
	}
	return &Volume{MountPoint: mount, VolumeName: vol}, nil
}

// EnumerateVolumes returns every volume known to the system, in
// enumeration order. Volumes without a mount point — the EFI System
// Partition, recovery partitions, letterless data volumes — appear with
// MountPoint == ""; they are still addressable (and snapshot-able, where
// the filesystem allows) via VolumeName.
func EnumerateVolumes() ([]*Volume, error) {
	if err := checkSupported(); err != nil {
		return nil, err
	}
	const maxVolumes = 4096 // bounded: nothing is unlimited
	buf := make([]uint16, windows.MAX_PATH+1)

	h, err := windows.FindFirstVolume(&buf[0], uint32(len(buf)))
	if err != nil {
		return nil, fmt.Errorf("vss: FindFirstVolume: %w", err)
	}
	defer windows.FindVolumeClose(h)

	var out []*Volume
	for len(out) < maxVolumes {
		name := windows.UTF16ToString(buf)
		if strings.HasPrefix(name, `\\?\Volume{`) {
			out = append(out, &Volume{
				VolumeName: name,
				MountPoint: firstMountPoint(name),
			})
		}
		if err := windows.FindNextVolume(h, &buf[0], uint32(len(buf))); err != nil {
			if err == windows.ERROR_NO_MORE_FILES {
				return out, nil
			}
			return nil, fmt.Errorf("vss: FindNextVolume: %w", err)
		}
	}
	return out, nil
}

// firstMountPoint resolves a volume's first mount path ("" if unmounted).
func firstMountPoint(volumeName string) string {
	np, err := utf16Ptr(volumeName) // API requires the trailing backslash form
	if err != nil {
		return ""
	}
	buf := make([]uint16, 32*1024/2)
	var n uint32
	if err := windows.GetVolumePathNamesForVolumeName(np, &buf[0], uint32(len(buf)), &n); err != nil {
		return ""
	}
	// MULTI_SZ: NUL-separated paths, double-NUL terminated.
	for i, c := range buf {
		if c == 0 {
			if i == 0 {
				return ""
			}
			return windows.UTF16ToString(buf[:i])
		}
	}
	return ""
}

// IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS and its reply layout
// (winioctl.h): DWORD NumberOfDiskExtents, pad, then DISK_EXTENT[]
// entries of {DWORD DiskNumber, pad, LONGLONG StartingOffset, LONGLONG
// ExtentLength}.
const ioctlVolumeGetVolumeDiskExtents = 0x00560000

type diskExtentRaw struct {
	DiskNumber     uint32
	_              [4]byte
	StartingOffset int64
	ExtentLength   int64
}

func init() {
	if unsafe.Sizeof(diskExtentRaw{}) != 24 {
		panic("vss: DISK_EXTENT layout is wrong")
	}
}

// DiskExtents maps the volume onto physical disks. Simple volumes return
// one extent; spanned/striped dynamic volumes return several.
func (v *Volume) DiskExtents() ([]DiskExtent, error) {
	base, err := v.devBase()
	if err != nil {
		return nil, err
	}
	h, err := openVolumeByPath(base)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(h)

	n := 8
	for {
		sz := 8 + n*24
		buf := make([]uint64, (sz+7)/8) // 8-aligned backing
		var ret uint32
		ioErr := windows.DeviceIoControl(h, ioctlVolumeGetVolumeDiskExtents,
			nil, 0, (*byte)(unsafe.Pointer(&buf[0])), uint32(sz), &ret, nil)
		count := int(*(*uint32)(unsafe.Pointer(&buf[0])))
		switch ioErr {
		case nil:
			if ret < 8 || count < 0 || count > (int(ret)-8)/24 {
				return nil, fmt.Errorf("vss: IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS: malformed reply (count %d, %d bytes)", count, ret)
			}
			out := make([]DiskExtent, count)
			for i := 0; i < count; i++ {
				e := (*diskExtentRaw)(unsafe.Add(unsafe.Pointer(&buf[0]), 8+i*24))
				out[i] = DiskExtent{DiskNumber: e.DiskNumber, StartingOffset: e.StartingOffset, Length: e.ExtentLength}
			}
			return out, nil
		case windows.ERROR_MORE_DATA, windows.ERROR_INSUFFICIENT_BUFFER:
			if count <= n { // no forward progress; fail closed
				return nil, fmt.Errorf("vss: IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS: buffer negotiation stuck at %d extents", n)
			}
			n = count
		default:
			return nil, fmt.Errorf("vss: IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS: %w", ioErr)
		}
	}
}

// devBase returns the volume address without the trailing backslash:
// the raw-volume / file-open base, mirroring Snapshot.DeviceObject.
func (v *Volume) devBase() (string, error) {
	if v.VolumeName == "" {
		return "", fmt.Errorf("vss: Volume has no volume name; use OpenVolume")
	}
	return strings.TrimSuffix(v.VolumeName, `\`), nil
}

// VolumeGeometry reports NTFS layout facts for the live volume. Cached
// per volume; note FreeClusters is a point-in-time reading.
func (v *Volume) VolumeGeometry() (VolumeGeometry, error) {
	base, err := v.devBase()
	if err != nil {
		return VolumeGeometry{}, err
	}
	return cachedVolumeGeometry(v.VolumeName, base)
}

// FileExtents maps a file's default data stream to physical byte ranges
// on the live volume, with the same semantics as Snapshot.FileExtents
// (resident files yield no extents, sparse holes omitted,
// cluster-granular lengths). The extents are only as stable as the file:
// concurrent writes can move or grow them.
func (v *Volume) FileExtents(rel string) ([]Extent, error) {
	base, err := v.devBase()
	if err != nil {
		return nil, err
	}
	geom, err := v.VolumeGeometry()
	if err != nil {
		return nil, err
	}
	return fileExtentsUnder(base, rel, geom.BytesPerCluster)
}

// Open opens a file or directory on the live volume by volume-relative
// path, with the same validation, backup semantics, and never-follow-
// reparse-points behavior as Snapshot.Open.
func (v *Volume) Open(rel string) (*os.File, error) {
	base, err := v.devBase()
	if err != nil {
		return nil, err
	}
	norm, err := validateVolumeRelativePath(rel)
	if err != nil {
		return nil, err
	}
	return openInSnapshot(base, norm, rel)
}
