//go:build windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"context"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	fsctlQueryUSNJournal = 0x000900f4
	fsctlReadUSNJournal  = 0x000900bb

	errorJournalDeleteInProgress = 1178
	errorJournalNotActive        = 1179
	errorJournalEntryDeleted     = 1181
)

// usnJournalDataV0 mirrors USN_JOURNAL_DATA_V0.
type usnJournalDataV0 struct {
	UsnJournalID    uint64
	FirstUsn        int64
	NextUsn         int64
	LowestValidUsn  int64
	MaxUsn          int64
	MaximumSize     uint64
	AllocationDelta uint64
}

// readUSNJournalDataV0 mirrors READ_USN_JOURNAL_DATA_V0.
type readUSNJournalDataV0 struct {
	StartUsn          int64
	ReasonMask        uint32
	ReturnOnlyOnClose uint32
	Timeout           uint64
	BytesToWaitFor    uint64
	UsnJournalID      uint64
}

// usnRecordV2 mirrors USN_RECORD_V2 (60-byte fixed header + name).
type usnRecordV2 struct {
	RecordLength              uint32
	MajorVersion              uint16
	MinorVersion              uint16
	FileReferenceNumber       uint64
	ParentFileReferenceNumber uint64
	Usn                       int64
	TimeStamp                 int64
	Reason                    uint32
	SourceInfo                uint32
	SecurityID                uint32
	FileAttributes            uint32
	FileNameLength            uint16
	FileNameOffset            uint16
}

// usnRecordV2Size is the fixed header before the inline name. Note
// unsafe.Sizeof(usnRecordV2{}) is 64 — Go rounds the struct size up to
// 8-byte alignment — so the assertion checks field offsets, which are
// what the buffer overlay actually depends on.
const usnRecordV2Size = 60

func init() {
	var r usnRecordV2
	if unsafe.Offsetof(r.FileReferenceNumber) != 8 ||
		unsafe.Offsetof(r.Usn) != 24 ||
		unsafe.Offsetof(r.FileAttributes) != 52 ||
		unsafe.Offsetof(r.FileNameOffset) != 58 {
		panic("vss: USN_RECORD_V2 layout is wrong")
	}
}

// openVolumeHandle opens the snapshot's raw volume for FSCTLs and
// open-by-ID (no trailing backslash on the device object).
func (s *Snapshot) openVolumeHandle() (windows.Handle, error) {
	if s.DeviceObject == "" {
		return windows.InvalidHandle, fmt.Errorf("vss: snapshot has no device object")
	}
	return openVolumeByPath(s.DeviceObject)
}

// openLiveVolumeHandle opens the snapshot's ORIGINAL (live) volume.
// NTFS refuses FSCTL_READ_USN_JOURNAL on shadow-copy volumes — journal
// reads are only served from the active journal — so the incremental read
// happens on the live volume, bounded by the snapshot's frozen position.
func (s *Snapshot) openLiveVolumeHandle() (windows.Handle, error) {
	if s.Volume == "" {
		return windows.InvalidHandle, fmt.Errorf("vss: snapshot has no original volume name")
	}
	return openVolumeByPath(strings.TrimSuffix(s.Volume, `\`))
}

func openVolumeByPath(path string) (windows.Handle, error) {
	p, err := utf16Ptr(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("vss: opening volume %s: %w", path, err)
	}
	return h, nil
}

// USNJournal queries the change journal state frozen inside this
// snapshot — informational only. Do NOT persist its Cursor() for
// incrementals: shadow-mounted journals round NextUSN up during recovery.
// Persist Snapshot.USNCursor instead, which was captured from the live
// volume just before the freeze.
func (s *Snapshot) USNJournal() (USNJournalInfo, error) {
	h, err := s.openVolumeHandle()
	if err != nil {
		return USNJournalInfo{}, err
	}
	defer windows.CloseHandle(h)
	return queryUSNJournal(h)
}

func queryUSNJournal(h windows.Handle) (USNJournalInfo, error) {
	var data usnJournalDataV0
	var ret uint32
	err := windows.DeviceIoControl(h, fsctlQueryUSNJournal,
		nil, 0,
		(*byte)(unsafe.Pointer(&data)), uint32(unsafe.Sizeof(data)), &ret, nil)
	if err != nil {
		if errno, ok := err.(windows.Errno); ok &&
			(errno == errorJournalNotActive || errno == errorJournalDeleteInProgress) {
			return USNJournalInfo{}, fmt.Errorf("vss: change journal not active on this volume: %w", ErrJournalReset)
		}
		return USNJournalInfo{}, fmt.Errorf("vss: FSCTL_QUERY_USN_JOURNAL: %w", err)
	}
	return USNJournalInfo{
		ID:              data.UsnJournalID,
		FirstUSN:        data.FirstUsn,
		NextUSN:         data.NextUsn,
		LowestValidUSN:  data.LowestValidUsn,
		MaxUSN:          data.MaxUsn,
		MaximumSize:     data.MaximumSize,
		AllocationDelta: data.AllocationDelta,
	}, nil
}

// captureLiveUSNCursor reads the live journal position of a volume, used
// by createSet just before the freeze. Zero cursor on any failure.
func captureLiveUSNCursor(volGUIDPath string) USNCursor {
	h, err := openVolumeByPath(strings.TrimSuffix(volGUIDPath, `\`))
	if err != nil {
		return USNCursor{}
	}
	defer windows.CloseHandle(h)
	info, err := queryUSNJournal(h)
	if err != nil {
		return USNCursor{}
	}
	return info.Cursor()
}

// USNChangesSince returns one aggregated entry per file changed between a
// previously persisted cursor and this snapshot, plus the cursor to
// persist for the next run (this snapshot's USNCursor).
//
// Records are read from the live volume's journal (NTFS does not serve
// journal reads from shadow copies), bounded by the position captured
// just before this snapshot froze. Files touched in the tiny window
// between capture and freeze are reported again on the next backup —
// safe over-reporting, never under-reporting. Paths and deletions are
// resolved inside the snapshot.
//
// Fail-closed contract: any condition that prevents a trustworthy
// incremental — journal recreated, wrapped past the cursor, cursor from
// another volume, unsupported record version (ReFS), or more than a
// million changed files — returns an error wrapping ErrJournalReset or
// ErrTooManyChanges. On those, do a full Walk; never treat the error as
// "no changes". See USNChange for the directory-rename caveat.
func (s *Snapshot) USNChangesSince(ctx context.Context, cursor USNCursor) ([]USNChange, USNCursor, error) {
	// The snapshot handle is for path/deletion resolution; the live
	// handle is where records are read, since NTFS only serves
	// FSCTL_READ_USN_JOURNAL from the active journal. The upper bound is
	// the cursor captured from the live volume just before the freeze.
	bound := s.USNCursor
	if bound == (USNCursor{}) {
		return nil, USNCursor{}, fmt.Errorf("vss: snapshot has no captured journal cursor (created without a journal, or discovered via List): %w", ErrJournalReset)
	}
	h, err := s.openVolumeHandle()
	if err != nil {
		return nil, USNCursor{}, err
	}
	defer windows.CloseHandle(h)
	live, err := s.openLiveVolumeHandle()
	if err != nil {
		return nil, USNCursor{}, err
	}
	defer windows.CloseHandle(live)

	liveInfo, err := queryUSNJournal(live)
	if err != nil {
		return nil, USNCursor{}, err
	}
	next := bound

	// Validate the cursor. Reject, don't repair: an invalid cursor means
	// the incremental chain is broken. Wrap detection uses the LIVE
	// FirstUSN — that's the journal the records come from, and it may
	// have wrapped after the snapshot was taken.
	switch {
	case cursor.JournalID != bound.JournalID:
		return nil, next, fmt.Errorf("vss: journal ID changed between backups (%#x -> %#x): %w", cursor.JournalID, bound.JournalID, ErrJournalReset)
	case liveInfo.ID != bound.JournalID:
		return nil, next, fmt.Errorf("vss: journal recreated after the snapshot (%#x -> %#x): %w", bound.JournalID, liveInfo.ID, ErrJournalReset)
	case cursor.NextUSN < liveInfo.FirstUSN:
		return nil, next, fmt.Errorf("vss: journal wrapped past cursor (usn %d < first %d): %w", cursor.NextUSN, liveInfo.FirstUSN, ErrJournalReset)
	case cursor.NextUSN > bound.NextUSN:
		return nil, next, fmt.Errorf("vss: cursor (usn %d) is ahead of this snapshot's captured position (%d); wrong volume or corrupt state: %w", cursor.NextUSN, bound.NextUSN, ErrJournalReset)
	case cursor.NextUSN%8 != 0 || cursor.NextUSN < 0:
		return nil, next, fmt.Errorf("vss: cursor usn %d is not record-aligned: %w", cursor.NextUSN, ErrJournalReset)
	}

	type agg struct {
		name    string
		parent  uint64
		reasons uint32
		attrs   uint32
		lastUSN int64
	}
	byID := make(map[uint64]*agg)

	in := readUSNJournalDataV0{
		StartUsn:     cursor.NextUSN,
		ReasonMask:   0xFFFFFFFF,
		UsnJournalID: liveInfo.ID,
	}
	// uint64 backing keeps the buffer 8-byte aligned for record access.
	buf := make([]uint64, 8192) // 64 KiB
	out := (*byte)(unsafe.Pointer(&buf[0]))

	done := false
	for !done {
		select {
		case <-ctx.Done():
			return nil, next, ctx.Err()
		default:
		}
		var ret uint32
		err := windows.DeviceIoControl(live, fsctlReadUSNJournal,
			(*byte)(unsafe.Pointer(&in)), uint32(unsafe.Sizeof(in)),
			out, uint32(len(buf)*8), &ret, nil)
		if err != nil {
			if errno, ok := err.(windows.Errno); ok && errno == errorJournalEntryDeleted {
				return nil, next, fmt.Errorf("vss: journal entries at cursor were purged: %w", ErrJournalReset)
			}
			return nil, next, fmt.Errorf("vss: FSCTL_READ_USN_JOURNAL (start=%d id=%#x bound=%d; live first=%d next=%d): %w",
				in.StartUsn, in.UsnJournalID, bound.NextUSN, liveInfo.FirstUSN, liveInfo.NextUSN, err)
		}
		if ret < 8 {
			break
		}
		nextStart := *(*int64)(unsafe.Pointer(&buf[0]))

		off := 8
		for off+usnRecordV2Size <= int(ret) {
			rec := (*usnRecordV2)(unsafe.Add(unsafe.Pointer(&buf[0]), off))
			rl := int(rec.RecordLength)
			if rl < usnRecordV2Size || off+rl > int(ret) {
				return nil, next, fmt.Errorf("vss: malformed USN record (length %d at offset %d): %w", rl, off, ErrJournalReset)
			}
			if rec.MajorVersion != 2 {
				// ReFS emits V3 records (128-bit IDs); not supported yet.
				return nil, next, fmt.Errorf("vss: USN record version %d unsupported (ReFS?): %w", rec.MajorVersion, ErrJournalReset)
			}
			if rec.Usn >= bound.NextUSN {
				// Past the snapshot's frozen position: these records
				// describe changes after the point in time being backed
				// up. They belong to the next incremental.
				done = true
				break
			}
			nameOff, nameLen := int(rec.FileNameOffset), int(rec.FileNameLength)
			if nameOff < usnRecordV2Size || nameOff+nameLen > rl || nameLen%2 != 0 {
				return nil, next, fmt.Errorf("vss: malformed USN record name (off %d len %d): %w", nameOff, nameLen, ErrJournalReset)
			}
			namePtr := (*uint16)(unsafe.Add(unsafe.Pointer(rec), nameOff))
			name := windows.UTF16ToString(unsafe.Slice(namePtr, nameLen/2))

			a := byID[rec.FileReferenceNumber]
			if a == nil {
				if len(byID) >= maxUSNChanges {
					return nil, next, fmt.Errorf("vss: over %d files changed since cursor: %w", maxUSNChanges, ErrTooManyChanges)
				}
				a = &agg{}
				byID[rec.FileReferenceNumber] = a
			}
			a.reasons |= rec.Reason
			// Keep the newest record's view of name/parent/attributes —
			// renames make earlier ones stale.
			a.name = name
			a.parent = rec.ParentFileReferenceNumber
			a.attrs = rec.FileAttributes
			a.lastUSN = rec.Usn
			off += rl
		}

		if nextStart <= in.StartUsn {
			break // no forward progress; journal is exhausted
		}
		in.StartUsn = nextStart
		if nextStart >= bound.NextUSN {
			break
		}
	}

	// Resolve paths inside the snapshot. Parent-directory resolutions are
	// cached: change sets cluster heavily by directory.
	r := &idResolver{vol: h, cache: make(map[uint64]resolved)}
	const fileAttributeDirectory = 0x10
	changes := make([]USNChange, 0, len(byID))
	for id, a := range byID {
		c := USNChange{
			Name:     a.name,
			IsDir:    a.attrs&fileAttributeDirectory != 0,
			FileID:   id,
			ParentID: a.parent,
			Reasons:  a.reasons,
			LastUSN:  a.lastUSN,
		}
		if path, err := r.pathByID(id); err == nil {
			c.Path = path
			c.PathKnown = true
		} else {
			// Not openable in the snapshot: the file is gone at this
			// point in time. Build its last-known path via the parent.
			c.Deleted = true
			if parentPath, perr := r.pathByID(a.parent); perr == nil {
				if parentPath == "" {
					c.Path = a.name
				} else {
					c.Path = parentPath + `\` + a.name
				}
				c.PathKnown = true
			}
		}
		changes = append(changes, c)
	}
	return changes, next, nil
}

type resolved struct {
	path string
	err  error
}

type idResolver struct {
	vol   windows.Handle
	cache map[uint64]resolved
}

func (r *idResolver) pathByID(id uint64) (string, error) {
	if got, ok := r.cache[id]; ok {
		return got.path, got.err
	}
	path, err := r.resolve(id)
	r.cache[id] = resolved{path, err}
	return path, err
}

// resolve opens the file by NTFS reference number inside the snapshot and
// asks for its volume-relative path (VOLUME_NAME_NONE — shadow devices
// have no DOS name to render).
func (r *idResolver) resolve(id uint64) (string, error) {
	h, err := openFileByID(r.vol, id)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)

	const volumeNameNone = 0x4 // VOLUME_NAME_NONE (not in x/sys)
	buf := make([]uint16, 512)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), volumeNameNone)
		if err != nil {
			return "", err
		}
		if int(n) <= len(buf) {
			return strings.TrimPrefix(windows.UTF16ToString(buf[:n]), `\`), nil
		}
		buf = make([]uint16, n+1)
	}
}

var (
	modKernel32      = windows.NewLazySystemDLL("kernel32.dll")
	procOpenFileById = modKernel32.NewProc("OpenFileById")
)

// fileIDDescriptor mirrors FILE_ID_DESCRIPTOR with the 64-bit FileId
// variant (Type 0); the union is 16 bytes.
type fileIDDescriptor struct {
	Size   uint32
	Type   uint32
	FileID uint64
	_      uint64
}

func openFileByID(vol windows.Handle, id uint64) (windows.Handle, error) {
	desc := fileIDDescriptor{
		Size:   uint32(unsafe.Sizeof(fileIDDescriptor{})),
		Type:   0, // FileIdType
		FileID: id,
	}
	const fileReadAttributes = 0x80
	h, _, lastErr := procOpenFileById.Call(
		uintptr(vol),
		uintptr(unsafe.Pointer(&desc)),
		fileReadAttributes,
		uintptr(windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE),
		0,
		uintptr(windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT),
	)
	if windows.Handle(h) == windows.InvalidHandle {
		if errno, ok := lastErr.(windows.Errno); ok && errno != 0 {
			return windows.InvalidHandle, errno
		}
		return windows.InvalidHandle, fmt.Errorf("OpenFileById failed")
	}
	return windows.Handle(h), nil
}
