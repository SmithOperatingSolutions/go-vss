//go:build windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/windows"
)

// resolveVolume converts a user path (drive root, mount point, any path on
// the volume, or an existing `\\?\Volume{...}\` name) into the canonical
// volume GUID path VSS prefers.
func resolveVolume(path string) (string, error) {
	vol, _, err := resolveVolumeAndMount(path)
	return vol, err
}

// resolveVolumeAndMount additionally returns the mount point the path
// resolved through (e.g. `C:\`); when the input already is a volume GUID
// path the mount point is that same path.
func resolveVolumeAndMount(path string) (volGUID, mountPoint string, err error) {
	if strings.HasPrefix(path, `\\?\Volume{`) {
		if !strings.HasSuffix(path, `\`) {
			path += `\`
		}
		return path, path, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("vss: resolving %q: %w", path, err)
	}
	if !strings.HasSuffix(abs, `\`) {
		abs += `\`
	}
	absp, err := utf16Ptr(abs)
	if err != nil {
		return "", "", err
	}
	mount := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumePathName(absp, &mount[0], uint32(len(mount))); err != nil {
		return "", "", fmt.Errorf("vss: GetVolumePathName(%q): %w", path, err)
	}
	volName := make([]uint16, 50+1) // \\?\Volume{GUID}\ is 49 chars
	if err := windows.GetVolumeNameForVolumeMountPoint(&mount[0], &volName[0], uint32(len(volName))); err != nil {
		return "", "", fmt.Errorf("vss: GetVolumeNameForVolumeMountPoint(%q): %w", path, err)
	}
	return windows.UTF16ToString(volName), windows.UTF16ToString(mount), nil
}

// createSet runs the canonical VSS requester lifecycle (reference doc §14)
// on a dedicated COM thread.
func createSet(ctx context.Context, volumePaths []string, cfg config) (*SnapshotSet, error) {
	if err := checkSupported(); err != nil {
		return nil, err
	}
	if err := enableBackupPrivilege(); err != nil {
		return nil, err
	}

	w, err := newWorker()
	if err != nil {
		return nil, err
	}

	var (
		bc      *backupComponents
		set     = &SnapshotSet{}
		snapIDs []guid
		setID   guid
	)

	err = w.do(func() error {
		var err error
		bc, err = createBackupComponents()
		if err != nil {
			return err
		}
		if err := bc.InitializeForBackup(); err != nil {
			return err
		}
		vssCtx := uint32(vssCtxBackup)
		switch {
		case cfg.persistent && cfg.noWriters:
			vssCtx = vssCtxNASRollback
		case cfg.persistent:
			vssCtx = vssCtxAppRollback
		case cfg.noWriters:
			vssCtx = vssCtxFileShareBackup
		}
		if err := bc.SetContext(vssCtx); err != nil {
			return err
		}
		// File-level posture: back up everything on the selected volumes,
		// no bootable system state, COPY semantics (never log-truncating).
		if err := bc.SetBackupState(false, false, vssBtCopy, false); err != nil {
			return err
		}
		if !cfg.noWriters {
			// Not optional: this is what makes writers aware a backup is
			// happening and lets them quiesce.
			a, err := bc.GatherWriterMetadata()
			if err != nil {
				return err
			}
			if err := awaitAsync(ctx, a, "GatherWriterMetadata", cfg.timeouts.GatherMetadata); err != nil {
				return err
			}
			// Exclude lists must be read before FreeWriterMetadata
			// invalidates the metadata interfaces.
			set.excludes, set.excludeErr = collectExcludeRules(bc)
			bc.FreeWriterMetadata()
		}

		// Resolve and pre-flight every volume before starting the set, so
		// unsupported volumes fail cleanly instead of mid-freeze.
		resolved := make([]string, len(volumePaths))
		for i, p := range volumePaths {
			vol, err := resolveVolume(p)
			if err != nil {
				return err
			}
			ok, err := bc.IsVolumeSupported(guidNull, vol)
			if err != nil {
				return fmt.Errorf("checking %q: %w", p, err)
			}
			if !ok {
				return fmt.Errorf("vss: volume %q (%s) does not support shadow copies", p, vol)
			}
			resolved[i] = vol
		}

		setID, err = bc.StartSnapshotSet()
		if err != nil {
			return err
		}
		for i, vol := range resolved {
			id, err := bc.AddToSnapshotSet(vol, guidNull)
			if err != nil {
				return fmt.Errorf("adding %q: %w", volumePaths[i], err)
			}
			snapIDs = append(snapIDs, id)
		}

		// PrepareForBackup requires GatherWriterMetadata to have run; in
		// the no-writers context both are skipped (as vshadow does) and
		// DoSnapshotSet is called directly.
		if !cfg.noWriters {
			a, err := bc.PrepareForBackup()
			if err != nil {
				return err
			}
			if err := awaitAsync(ctx, a, "PrepareForBackup", cfg.timeouts.Prepare); err != nil {
				return err
			}
		}

		// From here on, writers are quiesced: any failure must AbortBackup
		// or they stay wedged until timeout (handled in the error path
		// below).

		// Capture each volume's live change-journal position as late as
		// possible before the freeze. This — not the journal state
		// queried from the shadow device later — is the safe incremental
		// cursor: shadow-mounted journals round NextUSN up during
		// recovery, past records the live journal hasn't written yet.
		// Best-effort: volumes without a journal get a zero cursor and
		// fall back to full scans.
		usnCursors := make([]USNCursor, len(resolved))
		for i, vol := range resolved {
			usnCursors[i] = captureLiveUSNCursor(vol)
		}

		// Reduce the odds of a GC pause landing inside the freeze window.
		runtime.GC()

		a, err := bc.DoSnapshotSet()
		if err != nil {
			return err
		}
		if err := awaitAsync(ctx, a, "DoSnapshotSet", cfg.timeouts.Snapshot); err != nil {
			return err
		}

		// Record per-writer outcomes; a failed writer degrades the backup
		// rather than silently succeeding.
		if !cfg.noWriters {
			if a, err := bc.GatherWriterStatus(); err == nil {
				if err := awaitAsync(ctx, a, "GatherWriterStatus", cfg.timeouts.GatherMetadata); err == nil {
					if n, err := bc.GetWriterStatusCount(); err == nil {
						for i := uint32(0); i < n; i++ {
							if ws, err := bc.GetWriterStatus(i); err == nil {
								set.writers = append(set.writers, ws)
							}
						}
					}
					bc.FreeWriterStatus()
				}
			}
		}

		// Fetch device paths. This doubles as the runtime ABI validation:
		// a wrong vtable slot or GUID call shape cannot produce a valid
		// GLOBALROOT device path here.
		for i, id := range snapIDs {
			prop, err := bc.GetSnapshotProperties(id)
			if err != nil {
				return fmt.Errorf("snapshot for %q: %w", volumePaths[i], err)
			}
			s := snapshotFromProp(prop)
			freeSnapshotProps(prop)
			s.VolumePath = volumePaths[i]
			s.USNCursor = usnCursors[i]
			if !strings.HasPrefix(s.DeviceObject, `\\?\GLOBALROOT\Device\`) {
				return fmt.Errorf("vss: snapshot device object %q is not a GLOBALROOT path; refusing (possible ABI/vtable mismatch on %s)", s.DeviceObject, runtime.GOARCH)
			}
			if s.State != StateCreated {
				return fmt.Errorf("vss: snapshot of %q is in state %s, not created", volumePaths[i], s.State)
			}
			set.snaps = append(set.snaps, &s)
		}
		return nil
	})

	if err != nil {
		// Error path: unwind writers, drop the partial set, retire the
		// thread. AbortBackup on a session with nothing to abort returns
		// VSS_E_BAD_STATE harmlessly.
		if bc != nil {
			w.do(func() error {
				bc.AbortBackup()
				bc.Release()
				return nil
			})
		}
		w.stop()
		return nil, err
	}

	set.ID = setID.String()
	set.backend = &winSet{
		w:          w,
		bc:         bc,
		setID:      setID,
		timeouts:   cfg.timeouts,
		noWriters:  cfg.noWriters,
		persistent: cfg.persistent,
	}
	return set, nil
}

// winSet keeps the components object (and with it the transient snapshots)
// alive until Close.
type winSet struct {
	w          *worker
	bc         *backupComponents
	setID      guid
	timeouts   Timeouts
	noWriters  bool
	persistent bool
}

func (s *winSet) close() error {
	var firstErr error
	err := s.w.do(func() error {
		// Tell writers the backup finished; skipping this strands them in
		// WAITING_FOR_BACKUP_COMPLETE. Without writers there is no backup
		// ceremony to complete.
		if !s.noWriters {
			if a, err := s.bc.BackupComplete(); err == nil {
				if err := awaitAsync(context.Background(), a, "BackupComplete", s.timeouts.Complete); err != nil {
					firstErr = err
				}
			} else {
				firstErr = err
			}
		}

		// Explicitly delete rather than relying on auto-release, so partial
		// failures are observable. Persistent sets are the exception:
		// surviving Close is their entire purpose — the caller deletes
		// them later with Delete.
		if !s.persistent {
			const vssObjectSnapshotSet = 2
			_, nonDeleted, err := s.bc.DeleteSnapshots(s.setID, vssObjectSnapshotSet, true)
			if err != nil {
				var ve *Error
				// OBJECT_NOT_FOUND just means auto-release beat us to it.
				if !(errors.As(err, &ve) && ve.HRESULT == hrObjectNotFound) && firstErr == nil {
					firstErr = fmt.Errorf("vss: deleting snapshot set (first survivor %s): %w", nonDeleted.String(), err)
				}
			}
		}
		s.bc.Release()
		return nil
	})
	if err != nil && firstErr == nil {
		firstErr = err
	}
	s.w.stop()
	return firstErr
}
