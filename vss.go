// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

// Package vss creates, lists, reads, and deletes Windows Volume Shadow
// Copy Service (VSS) snapshots using a pure-Go COM requester (no cgo).
//
// The package abstracts the OS details away: on Windows (amd64/arm64) it
// talks directly to vssapi.dll via manual vtable dispatch; on every other
// platform it compiles cleanly and returns ErrUnsupported at runtime, so
// backup engines can depend on it unconditionally.
//
// Typical use:
//
//	set, err := vss.Create(ctx, `C:\`)
//	if err != nil { ... }
//	defer set.Close()
//
//	snap := set.Snapshots()[0]
//	data, err := fs.ReadFile(snap.FS(), "Windows/System32/drivers/etc/hosts")
//
// Requirements on Windows: the process must run elevated (Administrators),
// and must be a native binary for the host architecture (no WOW64).
package vss

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// SnapshotState mirrors VSS_SNAPSHOT_STATE. Only StateCreated is usable.
type SnapshotState int32

const (
	StateUnknown SnapshotState = 0
	StateCreated SnapshotState = 12
	StateAborted SnapshotState = 13
	StateDeleted SnapshotState = 14
)

func (s SnapshotState) String() string {
	switch s {
	case StateCreated:
		return "created"
	case StateAborted:
		return "aborted"
	case StateDeleted:
		return "deleted"
	default:
		return fmt.Sprintf("state(%d)", int32(s))
	}
}

// Snapshot describes one shadow copy of one volume.
type Snapshot struct {
	// ID is the shadow copy ID in "{xxxxxxxx-....}" form.
	ID string
	// SetID is the shadow copy set this snapshot belongs to.
	SetID string
	// Volume is the canonical original volume name, e.g. `\\?\Volume{...}\`.
	Volume string
	// VolumePath is the user-supplied path the snapshot was requested for
	// (e.g. `C:\`). Empty for snapshots discovered via List.
	VolumePath string
	// DeviceObject is the read path of the shadow copy, e.g.
	// `\\?\GLOBALROOT\Device\HarddiskVolumeShadowCopy37`.
	DeviceObject string
	// CreatedAt is the snapshot creation time (UTC).
	CreatedAt time.Time
	// Attributes is the VSS_VOLUME_SNAPSHOT_ATTRIBUTES bitmask.
	Attributes uint32
	// State is the snapshot state; only StateCreated snapshots are readable.
	State SnapshotState
	// USNCursor is the volume's change-journal position captured from the
	// LIVE volume immediately before the snapshot was frozen. This is the
	// cursor to persist after a successful backup from this snapshot: a
	// few pre-freeze records may land after it (they are re-reported next
	// run — safe), but it can never point past records the live journal
	// has yet to write (which would silently skip changes).
	//
	// Zero when the journal was unavailable at creation or the snapshot
	// was discovered via List; incremental reads then require a full scan.
	USNCursor USNCursor
}

// WriterState mirrors VSS_WRITER_STATE.
type WriterState int32

const (
	WriterUnknown                  WriterState = 0
	WriterStable                   WriterState = 1
	WriterWaitingForBackupComplete WriterState = 5
)

// Failed reports whether the state is one of the VSS_WS_FAILED_AT_* values.
func (w WriterState) Failed() bool { return w >= 6 && w <= 15 }

func (w WriterState) String() string {
	switch {
	case w == WriterStable:
		return "stable"
	case w == WriterWaitingForBackupComplete:
		return "waiting-for-backup-complete"
	case w.Failed():
		return fmt.Sprintf("failed(%d)", int32(w))
	default:
		return fmt.Sprintf("writer-state(%d)", int32(w))
	}
}

// WriterStatus is the per-writer outcome of a snapshot operation.
type WriterStatus struct {
	InstanceID string
	WriterID   string
	Name       string
	State      WriterState
	// Failure is non-nil when the writer reported a failure HRESULT.
	Failure error
}

// setBackend is the platform half of a SnapshotSet.
type setBackend interface {
	close() error
}

// SnapshotSet is a live shadow copy set created by Create/CreateSet.
//
// The snapshots exist for the lifetime of the set (transient VSS_CTX_BACKUP
// context): Close deletes them, and they are also released automatically if
// the process exits. A SnapshotSet must be Closed exactly once.
type SnapshotSet struct {
	// ID is the shadow copy set ID.
	ID string

	snaps      []*Snapshot
	writers    []WriterStatus
	excludes   []ExcludeRule
	excludeErr error
	backend    setBackend

	mu     sync.Mutex
	closed bool
}

// Snapshots returns the snapshots in the set, in the order the volumes were
// requested.
func (ss *SnapshotSet) Snapshots() []*Snapshot { return ss.snaps }

// ForVolume returns the snapshot created for the given requested volume path
// (as passed to Create/CreateSet), or nil.
func (ss *SnapshotSet) ForVolume(volumePath string) *Snapshot {
	for _, s := range ss.snaps {
		if s.VolumePath == volumePath {
			return s
		}
	}
	return nil
}

// WriterStatuses returns the state of every VSS writer after the snapshot
// was taken. Empty when the set was created with WithoutWriters.
func (ss *SnapshotSet) WriterStatuses() []WriterStatus { return ss.writers }

// ExcludeRules returns the exclude-file declarations gathered from writer
// metadata when the set was created: files writers say must not be backed
// up by naive copy (live databases, pagefile-class content, temp files).
// Honoring them makes backups smaller and restores safer.
//
// The error is non-nil when some writers' metadata could not be read — the
// returned rules are then incomplete but still usable. Empty when the set
// was created with WithoutWriters.
func (ss *SnapshotSet) ExcludeRules() ([]ExcludeRule, error) {
	return ss.excludes, ss.excludeErr
}

// Degraded returns the writers that reported failure. A snapshot with
// degraded writers is still readable and file-consistent, but the failed
// writers' application data (e.g. a database) may not be consistent inside
// it. Backup engines should surface this, not ignore it.
func (ss *SnapshotSet) Degraded() []WriterStatus {
	var out []WriterStatus
	for _, w := range ss.writers {
		if w.State.Failed() || w.Failure != nil {
			out = append(out, w)
		}
	}
	return out
}

// Verify re-queries VSS and confirms every snapshot in the set still exists
// in the created state. Call it periodically during long reads: when the
// copy-on-write diff area fills up, Windows silently deletes the oldest
// shadow copies, and a backup that lost its snapshot mid-run is invalid.
func (ss *SnapshotSet) Verify(ctx context.Context) error {
	ss.mu.Lock()
	if ss.closed {
		ss.mu.Unlock()
		return fmt.Errorf("vss: snapshot set is closed")
	}
	ss.mu.Unlock()

	infos, err := List(ctx)
	if err != nil {
		return fmt.Errorf("vss: verify: %w", err)
	}
	byID := make(map[string]Snapshot, len(infos))
	for _, s := range infos {
		byID[s.ID] = s
	}
	for _, s := range ss.snaps {
		got, ok := byID[s.ID]
		if !ok {
			return fmt.Errorf("vss: snapshot %s of %s no longer exists (diff area exhausted?); the backup is invalid", s.ID, s.VolumePath)
		}
		if got.State != StateCreated {
			return fmt.Errorf("vss: snapshot %s of %s is in state %s, not created", s.ID, s.VolumePath, got.State)
		}
	}
	return nil
}

// Close completes the backup session (notifying writers) and deletes the
// snapshots. It is safe to call once; subsequent calls return nil.
func (ss *SnapshotSet) Close() error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.closed {
		return nil
	}
	ss.closed = true
	return ss.backend.close()
}

// Create snapshots a single volume. See CreateSet.
func Create(ctx context.Context, volumePath string, opts ...Option) (*SnapshotSet, error) {
	return CreateSet(ctx, []string{volumePath}, opts...)
}

// CreateSet creates one shadow copy set covering all given volumes
// atomically (all snapshots are from the same instant). Each entry may be a
// drive root (`C:\`), a mount point, any path on the volume, or a canonical
// `\\?\Volume{...}\` name. VSS limits a set to 64 volumes.
//
// The default posture is file-consistent: writers participate and flush
// (VSS_CTX_BACKUP, VSS_BT_COPY — never log-truncating). Use WithoutWriters
// for crash-consistent snapshots without writer involvement.
func CreateSet(ctx context.Context, volumePaths []string, opts ...Option) (*SnapshotSet, error) {
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}
	if len(volumePaths) == 0 {
		return nil, fmt.Errorf("vss: no volumes given")
	}
	if len(volumePaths) > 64 {
		return nil, fmt.Errorf("vss: %d volumes exceeds the VSS limit of 64 per snapshot set", len(volumePaths))
	}
	return createSet(ctx, volumePaths, cfg)
}

// List returns all shadow copies visible on the system, across all snapshot
// contexts (equivalent to `vssadmin list shadows`). Requires elevation.
func List(ctx context.Context) ([]Snapshot, error) {
	return listSnapshots(ctx)
}

// ErrSnapshotNotFound is returned by Attach and Delete when no shadow
// copy with the given ID exists — it was deleted, or Windows evicted it
// under shadow-storage pressure. For a resume flow, the correct response
// is to refuse the resume and restart the backup.
var ErrSnapshotNotFound = errors.New("vss: snapshot not found (deleted or evicted)")

// Attach re-opens an existing shadow copy by snapshot ID so its device
// object can be read again — the resume path for persistent snapshots
// (WithPersistent) after the creating process died. Read-only: no writer
// session is re-established, WriterStatuses/ExcludeRules are empty, and
// USNCursor is zero (the resuming engine restores it from its own
// checkpoint). Close on the returned set releases nothing and deletes
// nothing; call Delete to remove the copy.
func Attach(ctx context.Context, snapshotID string) (*SnapshotSet, error) {
	g, err := parseGUID(snapshotID)
	if err != nil {
		return nil, err
	}
	want := g.String()
	snaps, err := List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range snaps {
		s := snaps[i]
		if s.ID != want {
			continue
		}
		if s.State != StateCreated {
			return nil, fmt.Errorf("vss: snapshot %s is in state %s, not created: %w", want, s.State, ErrSnapshotNotFound)
		}
		return &SnapshotSet{
			ID:      s.SetID,
			snaps:   []*Snapshot{&s},
			backend: attachedBackend{},
		}, nil
	}
	return nil, fmt.Errorf("vss: no shadow copy with ID %s: %w", want, ErrSnapshotNotFound)
}

// attachedBackend: an attached set owns no COM session and never deletes.
type attachedBackend struct{}

func (attachedBackend) close() error { return nil }

// Delete removes a shadow copy by ID — the cleanup half of the
// persistent-snapshot lifecycle. Returns ErrSnapshotNotFound (wrapped)
// if it no longer exists.
func Delete(ctx context.Context, snapshotID string) error {
	g, err := parseGUID(snapshotID)
	if err != nil {
		return err
	}
	return deleteSnapshotByID(ctx, g)
}
