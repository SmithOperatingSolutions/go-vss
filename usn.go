// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import "errors"

// ErrJournalReset means incremental change detection is not possible from
// the stored cursor: the journal was recreated, wrapped past the cursor,
// or the cursor doesn't belong to this volume. The only correct response
// is a full Walk — never treat "no usable journal" as "no changes"; that
// is the classic incremental-backup corruption bug.
var ErrJournalReset = errors.New("vss: change journal unusable from the stored cursor; full scan required")

// USNCursor is a resumable position in a volume's NTFS change journal.
// Persist Snapshot.USNCursor (equivalently, the cursor returned by
// USNChangesSince) with each completed backup, and feed it back on the
// next run. Do not build cursors from a journal query against a shadow
// device: shadow-mounted journals round NextUSN up during recovery, past
// records the live journal has not written yet, which silently skips
// changes.
type USNCursor struct {
	JournalID uint64 `json:"journal_id"`
	NextUSN   int64  `json:"next_usn"`
}

// USNJournalInfo describes the state of a volume's change journal as of
// the snapshot it was queried from.
type USNJournalInfo struct {
	ID              uint64
	FirstUSN        int64
	NextUSN         int64
	LowestValidUSN  int64
	MaxUSN          int64
	MaximumSize     uint64
	AllocationDelta uint64
}

// Cursor returns this journal state as a cursor. Only trustworthy when
// the info was queried from a LIVE volume — see the USNCursor warning
// about shadow-device queries. For backups, use Snapshot.USNCursor.
func (i USNJournalInfo) Cursor() USNCursor {
	return USNCursor{JournalID: i.ID, NextUSN: i.NextUSN}
}

// USNChange is one file or directory the journal reports as touched since
// the cursor, aggregated per file ID (Reasons is the OR of every record).
//
// Deleted reflects presence in the *snapshot being read*: the file ID
// could not be opened there, so the file no longer exists at the point in
// time being backed up.
//
// Directory-rename caveat: renaming a directory produces records for the
// directory only — nothing for the files beneath it, whose paths all
// changed. Engines matching manifests by path must treat a renamed
// directory as invalidating its whole subtree (re-walk it, or match by
// FileID instead of path).
type USNChange struct {
	// Path is the volume-relative path resolved inside the snapshot
	// (e.g. `Users\bob\file.txt`); valid only when PathKnown.
	Path string
	// PathKnown is false when neither the file nor its parent directory
	// could be resolved (e.g. a file created and deleted inside a
	// directory that was also deleted). Match by FileID, or fall back to
	// a full scan for safety.
	PathKnown bool
	// Name is the file's final name as recorded in the journal.
	Name string
	// IsDir reports whether the journal records mark it as a directory.
	IsDir bool
	// Deleted reports that the file ID no longer exists in the snapshot.
	Deleted bool
	// FileID and ParentID are NTFS file reference numbers.
	FileID   uint64
	ParentID uint64
	// Reasons is the OR of all USNReason* bits observed.
	Reasons uint32
	// LastUSN is the USN of the newest record for this file.
	LastUSN int64
}

// USN_REASON_* bits (winioctl.h).
const (
	USNReasonDataOverwrite      = 0x00000001
	USNReasonDataExtend         = 0x00000002
	USNReasonDataTruncation     = 0x00000004
	USNReasonNamedDataOverwrite = 0x00000010
	USNReasonNamedDataExtend    = 0x00000020
	USNReasonNamedDataTrunc     = 0x00000040
	USNReasonFileCreate         = 0x00000100
	USNReasonFileDelete         = 0x00000200
	USNReasonEAChange           = 0x00000400
	USNReasonSecurityChange     = 0x00000800
	USNReasonRenameOldName      = 0x00001000
	USNReasonRenameNewName      = 0x00002000
	USNReasonIndexableChange    = 0x00004000
	USNReasonBasicInfoChange    = 0x00008000
	USNReasonHardLinkChange     = 0x00010000
	USNReasonCompressionChange  = 0x00020000
	USNReasonEncryptionChange   = 0x00040000
	USNReasonObjectIDChange     = 0x00080000
	USNReasonReparsePointChange = 0x00100000
	USNReasonStreamChange       = 0x00200000
	USNReasonTransactedChange   = 0x00400000
	USNReasonIntegrityChange    = 0x00800000
	USNReasonClose              = 0x80000000
)

// maxUSNChanges bounds how many distinct changed files USNChangesSince
// will aggregate. Beyond this a full scan is almost certainly cheaper
// than incremental processing, and nothing in this package is unbounded.
const maxUSNChanges = 1 << 20

// ErrTooManyChanges is returned (wrapped) when the journal reports more
// than maxUSNChanges distinct files; respond with a full scan.
var ErrTooManyChanges = errors.New("vss: too many changes since cursor; full scan required")
