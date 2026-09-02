// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import "time"

// Timeouts bounds each asynchronous VSS phase. VSS operations can hang
// indefinitely on a wedged writer; every wait in this package is bounded.
type Timeouts struct {
	// GatherMetadata bounds IVssBackupComponents::GatherWriterMetadata.
	GatherMetadata time.Duration
	// Prepare bounds PrepareForBackup.
	Prepare time.Duration
	// Snapshot bounds DoSnapshotSet (includes the writer freeze window).
	Snapshot time.Duration
	// Complete bounds BackupComplete.
	Complete time.Duration
}

func defaultTimeouts() Timeouts {
	return Timeouts{
		GatherMetadata: 120 * time.Second,
		Prepare:        120 * time.Second,
		Snapshot:       180 * time.Second,
		Complete:       120 * time.Second,
	}
}

type config struct {
	timeouts   Timeouts
	noWriters  bool
	persistent bool
}

func defaultConfig() config {
	return config{timeouts: defaultTimeouts()}
}

// Option configures Create/CreateSet.
type Option func(*config)

// WithTimeouts overrides the per-phase timeouts. Zero fields keep their
// defaults.
func WithTimeouts(t Timeouts) Option {
	return func(c *config) {
		d := defaultTimeouts()
		if t.GatherMetadata > 0 {
			d.GatherMetadata = t.GatherMetadata
		}
		if t.Prepare > 0 {
			d.Prepare = t.Prepare
		}
		if t.Snapshot > 0 {
			d.Snapshot = t.Snapshot
		}
		if t.Complete > 0 {
			d.Complete = t.Complete
		}
		c.timeouts = d
	}
}

// WithoutWriters requests a crash-consistent snapshot with no VSS writer
// participation (VSS_CTX_FILE_SHARE_BACKUP). Faster and immune to broken
// writers, but databases captured in the snapshot may be mid-transaction.
func WithoutWriters() Option {
	return func(c *config) { c.noWriters = true }
}

// WithPersistent creates shadow copies that survive both Close and the
// requester process (persistent snapshot context) — for backups that
// must resume after a crash. The caller owns the full cleanup problem:
// Close releases the session WITHOUT deleting the copies; re-open them
// later with Attach and remove them with Delete. A persistent copy pins
// shadow-storage space until deleted, and Windows can still evict it
// under diff-area pressure — Attach fails loudly in that case.
func WithPersistent() Option {
	return func(c *config) { c.persistent = true }
}
