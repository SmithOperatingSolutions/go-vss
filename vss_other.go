//go:build !windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"context"
	"io/fs"
	"os"
)

func createSet(ctx context.Context, volumePaths []string, cfg config) (*SnapshotSet, error) {
	return nil, ErrUnsupported
}

func listSnapshots(ctx context.Context) ([]Snapshot, error) {
	return nil, ErrUnsupported
}

func deleteSnapshotByID(ctx context.Context, id guid) error {
	return ErrUnsupported
}

// FS returns a filesystem over the snapshot. On non-Windows platforms every
// operation on the returned fs.FS fails with ErrUnsupported.
func (s *Snapshot) FS() fs.FS { return errFS{} }

// Open opens a file inside the snapshot by volume-relative path.
// Not supported on this platform.
func (s *Snapshot) Open(rel string) (*os.File, error) { return nil, ErrUnsupported }

// OpenRaw opens the snapshot's raw block device. Not supported on this
// platform.
func (s *Snapshot) OpenRaw() (*os.File, error) { return nil, ErrUnsupported }

type errFS struct{}

func (errFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: ErrUnsupported}
}
