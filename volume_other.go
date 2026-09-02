//go:build !windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import "os"

// OpenVolume resolves a live volume for extent/geometry/file queries.
// Not supported on this platform.
func OpenVolume(path string) (*Volume, error) { return nil, ErrUnsupported }

// EnumerateVolumes returns every volume known to the system. Not
// supported on this platform.
func EnumerateVolumes() ([]*Volume, error) { return nil, ErrUnsupported }

// DiskExtents maps the volume onto physical disks. Not supported on this
// platform.
func (v *Volume) DiskExtents() ([]DiskExtent, error) { return nil, ErrUnsupported }

// VolumeGeometry reports NTFS layout facts. Not supported on this
// platform.
func (v *Volume) VolumeGeometry() (VolumeGeometry, error) {
	return VolumeGeometry{}, ErrUnsupported
}

// FileExtents maps a file to physical byte ranges. Not supported on this
// platform.
func (v *Volume) FileExtents(rel string) ([]Extent, error) { return nil, ErrUnsupported }

// Open opens a file on the live volume. Not supported on this platform.
func (v *Volume) Open(rel string) (*os.File, error) { return nil, ErrUnsupported }
