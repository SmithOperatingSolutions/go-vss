//go:build windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"context"
	"errors"
	"fmt"
)

// listSnapshots enumerates all shadow copies via a dedicated, single-use
// components object (a Query'd object cannot be reused for a backup).
func listSnapshots(ctx context.Context) ([]Snapshot, error) {
	if err := checkSupported(); err != nil {
		return nil, err
	}
	w, err := newWorker()
	if err != nil {
		return nil, err
	}
	defer w.stop()

	var out []Snapshot
	err = w.do(func() error {
		bc, err := createBackupComponents()
		if err != nil {
			return err
		}
		defer bc.Release()

		if err := bc.InitializeForBackup(); err != nil {
			return err
		}
		// VSS_CTX_ALL: include persistent and client-accessible copies,
		// not just the transient backup context.
		if err := bc.SetContext(vssCtxAll); err != nil {
			return err
		}
		enum, err := bc.Query(vssObjectSnapshot)
		if err != nil {
			return err
		}
		defer enum.Release()

		// Bounded batches with a hard cap: never allocate or loop on an
		// untrusted count.
		const (
			batch        = 16
			maxSnapshots = 4096
		)
		for len(out) < maxSnapshots {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			var props [batch]vssObjectProp
			n, done, err := enum.Next(props[:])
			if err != nil {
				return err
			}
			for i := 0; i < int(n); i++ {
				snap := props[i].snapshot()
				if snap == nil {
					continue // wrong union variant; never reinterpret
				}
				out = append(out, snapshotFromProp(snap))
				freeSnapshotProps(snap) // free immediately after copying
			}
			if done || n == 0 {
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// deleteSnapshotByID removes one shadow copy via a dedicated single-use
// components object.
func deleteSnapshotByID(ctx context.Context, id guid) error {
	if err := checkSupported(); err != nil {
		return err
	}
	w, err := newWorker()
	if err != nil {
		return err
	}
	defer w.stop()

	return w.do(func() error {
		bc, err := createBackupComponents()
		if err != nil {
			return err
		}
		defer bc.Release()

		if err := bc.InitializeForBackup(); err != nil {
			return err
		}
		if err := bc.SetContext(vssCtxAll); err != nil {
			return err
		}
		deleted, _, err := bc.DeleteSnapshots(id, vssObjectSnapshot, true)
		if err != nil {
			var ve *Error
			if errors.As(err, &ve) && ve.HRESULT == hrObjectNotFound {
				return fmt.Errorf("vss: deleting %s: %w", id.String(), ErrSnapshotNotFound)
			}
			return err
		}
		if deleted != 1 {
			return fmt.Errorf("vss: DeleteSnapshots(%s) reported %d deletions, want 1", id.String(), deleted)
		}
		return nil
	})
}
