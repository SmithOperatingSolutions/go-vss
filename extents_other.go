//go:build !windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

// VolumeGeometry reports NTFS layout facts for the snapshot volume. Not
// supported on this platform.
func (s *Snapshot) VolumeGeometry() (VolumeGeometry, error) {
	return VolumeGeometry{}, ErrUnsupported
}

// FileExtents returns the physical extents of a file within the snapshot.
// Not supported on this platform.
func (s *Snapshot) FileExtents(rel string) ([]Extent, error) {
	return nil, ErrUnsupported
}
