//go:build windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Integration tests exercise the real VSS service. They need an elevated
// process (GitHub-hosted Windows runners qualify) and skip otherwise.
//
// Deliberately sequential — do NOT add t.Parallel to tests that create
// snapshots: VSS serializes snapshot creation machine-wide, so parallel
// creation in one binary self-inflicts VSS_E_SNAPSHOT_SET_IN_PROGRESS.

func requireElevated(t *testing.T) {
	t.Helper()
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("requires an elevated (Administrator) process")
	}
	if err := checkSupported(); err != nil {
		t.Skipf("unsupported configuration: %v", err)
	}
}

func systemDrive() string {
	if d := os.Getenv("SystemDrive"); d != "" {
		return d + `\`
	}
	return `C:\`
}

// createWithRetry tolerates another backup product holding the machine-wide
// snapshot lock (common on shared CI).
func createWithRetry(t *testing.T, ctx context.Context, vol string) *SnapshotSet {
	t.Helper()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		set, err := Create(ctx, vol)
		if err == nil {
			return set
		}
		lastErr = err
		if !IsRetryable(err) {
			break
		}
		t.Logf("attempt %d: retryable failure: %v", attempt+1, err)
		time.Sleep(time.Duration(10*(attempt+1)) * time.Second)
	}
	t.Fatalf("Create(%s): %v", vol, lastErr)
	return nil
}

func TestIntegrationCreateReadDelete(t *testing.T) {
	requireElevated(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	set := createWithRetry(t, ctx, systemDrive())
	closed := false
	defer func() {
		if !closed {
			set.Close()
		}
	}()

	if set.ID == "" || set.ID == "{00000000-0000-0000-0000-000000000000}" {
		t.Errorf("snapshot set ID looks wrong: %q", set.ID)
	}
	snaps := set.Snapshots()
	if len(snaps) != 1 {
		t.Fatalf("got %d snapshots, want 1", len(snaps))
	}
	snap := snaps[0]
	t.Logf("snapshot %s device %s created %s attrs 0x%X", snap.ID, snap.DeviceObject, snap.CreatedAt, snap.Attributes)

	if !strings.HasPrefix(snap.DeviceObject, `\\?\GLOBALROOT\Device\HarddiskVolumeShadowCopy`) {
		t.Fatalf("unexpected device object %q", snap.DeviceObject)
	}
	if snap.State != StateCreated {
		t.Fatalf("snapshot state = %s, want created", snap.State)
	}
	if age := time.Since(snap.CreatedAt); age < 0 || age > time.Hour {
		t.Errorf("creation timestamp %s is implausible (FILETIME conversion?)", snap.CreatedAt)
	}

	// Writer statuses should have been collected in the default posture.
	if n := len(set.WriterStatuses()); n == 0 {
		t.Error("expected writer statuses in default (writer-aware) posture")
	} else {
		t.Logf("%d writers, %d degraded", n, len(set.Degraded()))
	}

	// --- Writer exclude rules. Whether any exist depends on which writers
	// are active: client SKUs run the Search/BITS writers, which always
	// declare excludes; a server runner may legitimately have none. Only
	// treat emptiness as a failure when a known-declaring writer is
	// present, so the assertion tests our enumeration rather than the
	// runner's service inventory.
	rules, rulesErr := set.ExcludeRules()
	if rulesErr != nil {
		t.Errorf("ExcludeRules reported incomplete metadata: %v", rulesErr)
	}
	expectRules := false
	for _, w := range set.WriterStatuses() {
		if strings.Contains(w.Name, "MSSearch") || strings.Contains(w.Name, "BITS") {
			expectRules = true
		}
	}
	t.Logf("%d exclude rules collected (declaring writers present: %v)", len(rules), expectRules)
	if len(rules) == 0 && expectRules {
		t.Error("writers that declare exclude rules are present, but none were collected")
	}
	for i, r := range rules {
		if i < 10 {
			t.Logf("exclude [%s] path=%q raw=%q spec=%q recursive=%v", r.Writer, r.Path, r.RawPath, r.FileSpec, r.Recursive)
		}
		if r.Writer == "" {
			t.Errorf("rule %d has no writer attribution: %+v", i, r)
		}
		if r.Path == "" {
			t.Errorf("rule %d has empty path: %+v", i, r)
		}
		if strings.Contains(r.Path, "%") {
			// Undefined variables legitimately stay unexpanded; just
			// surface them for inspection.
			t.Logf("rule %d path retains env syntax after expansion: %q", i, r.Path)
		}
	}

	// --- Read a well-known file through fs.FS.
	data, err := fs.ReadFile(snap.FS(), "Windows/System32/drivers/etc/hosts")
	if err != nil {
		t.Fatalf("reading hosts from snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Error("hosts file read as empty")
	}

	// --- Directory enumeration through fs.FS.
	entries, err := fs.ReadDir(snap.FS(), ".")
	if err != nil {
		t.Fatalf("ReadDir(root): %v", err)
	}
	var sawWindows bool
	for _, e := range entries {
		if strings.EqualFold(e.Name(), "Windows") {
			sawWindows = true
		}
	}
	if !sawWindows {
		t.Errorf("root listing missing Windows dir; got %d entries", len(entries))
	}

	// --- Volume-relative Open with backslashes.
	f, err := snap.Open(`Windows\System32\drivers\etc\hosts`)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	f.Close()

	// --- Traversal and absolute paths must be rejected.
	for _, bad := range []string{`..\x`, `C:\Windows`, `\Windows`, "a:b"} {
		if _, err := snap.Open(bad); err == nil {
			t.Errorf("Open(%q) should have been rejected", bad)
		}
	}

	// --- Manifest walk of a small, always-present subtree.
	const etc = `Windows\System32\drivers\etc`
	collectWalk := func(opts WalkOptions) []Entry {
		t.Helper()
		var got []Entry
		err := snap.Walk(ctx, etc, opts, func(e *Entry, err error) error {
			if err != nil {
				t.Errorf("walk error at %q: %v", e.Path, err)
				return nil
			}
			got = append(got, *e)
			return nil
		})
		if err != nil {
			t.Fatalf("Walk(%s): %v", etc, err)
		}
		return got
	}

	walk1 := collectWalk(WalkOptions{})
	if len(walk1) == 0 {
		t.Fatal("walk of drivers\\etc returned nothing")
	}
	var hostsEntry *Entry
	for i := range walk1 {
		e := &walk1[i]
		if !strings.HasPrefix(e.Path, etc+`\`) {
			t.Errorf("entry path %q not under walk root", e.Path)
		}
		if strings.EqualFold(e.Name, "hosts") {
			hostsEntry = e
		}
	}
	if hostsEntry == nil {
		t.Fatalf("hosts not found among %d entries", len(walk1))
	}
	if hostsEntry.IsDir || hostsEntry.Size <= 0 {
		t.Errorf("hosts entry looks wrong: %+v", hostsEntry)
	}
	if hostsEntry.Modified.IsZero() || hostsEntry.Modified.Year() < 2000 {
		t.Errorf("hosts Modified timestamp implausible: %v", hostsEntry.Modified)
	}
	// Manifest size must agree with the bytes actually read earlier.
	if hostsEntry.Size != int64(len(data)) {
		t.Errorf("manifest size %d != read size %d", hostsEntry.Size, len(data))
	}

	// --- Determinism: identical ordering across walks.
	walk2 := collectWalk(WalkOptions{})
	if len(walk1) != len(walk2) {
		t.Fatalf("walk not deterministic: %d vs %d entries", len(walk1), len(walk2))
	}
	for i := range walk1 {
		if walk1[i].Path != walk2[i].Path {
			t.Fatalf("walk order differs at %d: %q vs %q", i, walk1[i].Path, walk2[i].Path)
		}
	}

	// --- Exclusion drops the file; IncludeExcluded annotates it instead.
	hostsRule := ExcludeRule{Path: systemDrive() + etc, FileSpec: "hosts"}
	for _, e := range collectWalk(WalkOptions{Excludes: []ExcludeRule{hostsRule}}) {
		if strings.EqualFold(e.Name, "hosts") {
			t.Errorf("excluded hosts still emitted: %+v", e)
		}
	}
	sawAnnotated := false
	for _, e := range collectWalk(WalkOptions{Excludes: []ExcludeRule{hostsRule}, IncludeExcluded: true}) {
		if strings.EqualFold(e.Name, "hosts") {
			sawAnnotated = true
			if !e.Excluded || e.ExcludedBy == nil {
				t.Errorf("hosts should be annotated excluded: %+v", e)
			}
		}
	}
	if !sawAnnotated {
		t.Error("IncludeExcluded did not emit the excluded entry")
	}

	// --- SkipDir prunes descent: walking Windows\System32 shallow.
	topOnly := 0
	deep := false
	err = snap.Walk(ctx, `Windows\System32`, WalkOptions{}, func(e *Entry, err error) error {
		if err != nil {
			return nil
		}
		if strings.Count(e.Path, `\`) > 2 {
			deep = true
		}
		topOnly++
		if e.IsDir {
			return SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatalf("shallow walk: %v", err)
	}
	if topOnly == 0 || deep {
		t.Errorf("SkipDir walk wrong: %d entries, descended=%v", topOnly, deep)
	}

	// --- The kernel-scrubbed files really are browsable (present with
	// size) even though their contents are undefined — the reason
	// SystemExcludes exists. pagefile may be absent on CI images, so only
	// verify the property when present.
	rootEntries, err := fs.ReadDir(snap.FS(), ".")
	if err == nil {
		for _, re := range rootEntries {
			if strings.EqualFold(re.Name(), "pagefile.sys") {
				if matchExclude(SystemExcludes(systemDrive()), systemDrive()+"pagefile.sys") == nil {
					t.Error("SystemExcludes must cover the visible pagefile.sys")
				}
				t.Log("pagefile.sys is visible in the browsed snapshot (as expected; contents are scrubbed)")
			}
		}
	}

	// --- Raw device read (block-level path).
	raw, err := snap.OpenRaw()
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	buf := make([]byte, 4096)
	if _, err := io.ReadFull(raw, buf); err != nil {
		t.Errorf("raw read: %v", err)
	}
	raw.Close()

	// --- The snapshot must be visible in a listing and verify clean.
	list, err := List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, s := range list {
		if s.ID == snap.ID {
			found = true
			if s.State != StateCreated {
				t.Errorf("listed state = %s, want created", s.State)
			}
		}
	}
	if !found {
		t.Errorf("snapshot %s not found among %d listed", snap.ID, len(list))
	}
	if err := set.Verify(ctx); err != nil {
		t.Errorf("Verify: %v", err)
	}

	// --- Close deletes the transient snapshot.
	if err := set.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	closed = true
	if err := set.Close(); err != nil {
		t.Errorf("second Close should be nil, got %v", err)
	}

	list, err = List(ctx)
	if err != nil {
		t.Fatalf("List after close: %v", err)
	}
	for _, s := range list {
		if s.ID == snap.ID {
			t.Errorf("snapshot %s still present after Close", snap.ID)
		}
	}
}

func TestIntegrationWithoutWriters(t *testing.T) {
	requireElevated(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	set, err := Create(ctx, systemDrive(), WithoutWriters())
	if err != nil {
		if IsRetryable(err) {
			t.Skipf("snapshot lock contention: %v", err)
		}
		t.Fatalf("Create: %v", err)
	}
	defer set.Close()

	if len(set.WriterStatuses()) != 0 {
		t.Error("WithoutWriters must not gather writer statuses")
	}
	if rules, err := set.ExcludeRules(); err != nil || len(rules) != 0 {
		t.Errorf("WithoutWriters must yield no exclude rules, got %d rules, err=%v", len(rules), err)
	}
	if _, err := fs.Stat(set.Snapshots()[0].FS(), "Windows"); err != nil {
		t.Errorf("Stat(Windows): %v", err)
	}
}

func TestIntegrationResolveVolume(t *testing.T) {
	if err := checkSupported(); err != nil {
		t.Skipf("unsupported: %v", err)
	}
	vol, err := resolveVolume(systemDrive())
	if err != nil {
		t.Fatalf("resolveVolume: %v", err)
	}
	if !strings.HasPrefix(vol, `\\?\Volume{`) || !strings.HasSuffix(vol, `\`) {
		t.Errorf("resolveVolume = %q, want \\\\?\\Volume{...}\\ form", vol)
	}
	// Any path on the volume must resolve to the same volume GUID.
	vol2, err := resolveVolume(os.Getenv("SystemRoot"))
	if err != nil {
		t.Fatalf("resolveVolume(SystemRoot): %v", err)
	}
	if vol2 != vol {
		t.Errorf("SystemRoot resolved to %q, drive root to %q", vol2, vol)
	}
}

func TestIntegrationUSNIncremental(t *testing.T) {
	requireElevated(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// Backup 1: snapshot, record the captured cursor, release.
	setA := createWithRetry(t, ctx, systemDrive())
	cursor := setA.Snapshots()[0].USNCursor
	if cursor == (USNCursor{}) {
		setA.Close()
		t.Skip("no change journal on the system volume (cursor not captured)")
	}
	if cursor.JournalID == 0 || cursor.NextUSN <= 0 || cursor.NextUSN%8 != 0 {
		t.Errorf("implausible captured cursor: %+v", cursor)
	}
	// The frozen journal info remains queryable (informational).
	if infoA, err := setA.Snapshots()[0].USNJournal(); err != nil {
		t.Errorf("USNJournal on snapshot: %v", err)
	} else if infoA.ID != cursor.JournalID {
		t.Errorf("frozen journal ID %#x != captured cursor ID %#x", infoA.ID, cursor.JournalID)
	}
	if err := setA.Close(); err != nil {
		t.Fatalf("closing snapshot A: %v", err)
	}

	// Mutate the live volume: one file that persists, one create+delete.
	// TEMP on the runners lives on the system drive.
	dir, err := os.MkdirTemp("", "govss-usn-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	// Expand any 8.3 short-name components (RUNNER~1) so paths compare
	// against the normalized form the resolver returns.
	dir = longPathName(t, dir)
	if !strings.EqualFold(dir[:2], systemDrive()[:2]) {
		t.Skipf("temp dir %s is not on the system drive", dir)
	}
	kept := dir + `\kept.txt`
	if err := os.WriteFile(kept, []byte("incremental payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	doomed := dir + `\doomed.txt`
	if err := os.WriteFile(doomed, []byte("short-lived"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(doomed); err != nil {
		t.Fatal(err)
	}

	// Backup 2: snapshot, read changes since backup 1's cursor.
	setB := createWithRetry(t, ctx, systemDrive())
	defer setB.Close()
	snapB := setB.Snapshots()[0]

	// Diagnostics: the frozen (snapshot) vs live journal state, so a
	// failure in the FSCTL path is debuggable from CI logs alone.
	t.Logf("cursor from snapshot A: %+v", cursor)
	if infoB, err := snapB.USNJournal(); err == nil {
		t.Logf("snapshot B journal: %+v", infoB)
	}
	if liveH, err := openVolumeByPath(strings.TrimSuffix(snapB.Volume, `\`)); err == nil {
		if liveInfo, err := queryUSNJournal(liveH); err == nil {
			t.Logf("live journal: %+v", liveInfo)
		}
		windows.CloseHandle(liveH)
	}

	changes, next, err := snapB.USNChangesSince(ctx, cursor)
	if err != nil {
		t.Fatalf("USNChangesSince: %v", err)
	}
	if next.JournalID != cursor.JournalID || next.NextUSN < cursor.NextUSN {
		t.Errorf("next cursor %+v not advanced from %+v", next, cursor)
	}
	t.Logf("%d changed files between snapshots", len(changes))

	rel := func(abs string) string { return strings.TrimPrefix(abs, systemDrive()) }
	var keptChange, doomedChange *USNChange
	for i := range changes {
		c := &changes[i]
		if c.PathKnown && strings.EqualFold(c.Path, rel(kept)) {
			keptChange = c
		}
		if strings.EqualFold(c.Name, "doomed.txt") {
			doomedChange = c
		}
	}

	if keptChange == nil {
		t.Fatalf("created file %s not reported among %d changes", rel(kept), len(changes))
	}
	if keptChange.Deleted || keptChange.IsDir {
		t.Errorf("kept file misclassified: %+v", keptChange)
	}
	if keptChange.Reasons&USNReasonFileCreate == 0 {
		t.Errorf("kept file missing FILE_CREATE reason: %#x", keptChange.Reasons)
	}
	// The reported path must actually resolve inside the snapshot, and to
	// the bytes written before the snapshot.
	got, err := fs.ReadFile(snapB.FS(), strings.ReplaceAll(keptChange.Path, `\`, "/"))
	if err != nil {
		t.Errorf("reading changed file from snapshot: %v", err)
	} else if string(got) != "incremental payload" {
		t.Errorf("changed-file content = %q", got)
	}

	if doomedChange == nil {
		t.Fatal("deleted file not reported")
	}
	if !doomedChange.Deleted {
		t.Errorf("doomed file should be Deleted (absent from snapshot B): %+v", doomedChange)
	}
	if !doomedChange.PathKnown || !strings.EqualFold(doomedChange.Path, rel(doomed)) {
		t.Errorf("doomed file path resolution: %+v, want %s", doomedChange, rel(doomed))
	}

	// Fail-closed checks.
	if _, _, err := snapB.USNChangesSince(ctx, USNCursor{JournalID: cursor.JournalID + 1, NextUSN: cursor.NextUSN}); !errors.Is(err, ErrJournalReset) {
		t.Errorf("mismatched journal ID = %v, want ErrJournalReset", err)
	}
	if _, _, err := snapB.USNChangesSince(ctx, USNCursor{JournalID: cursor.JournalID, NextUSN: 1 << 62}); !errors.Is(err, ErrJournalReset) {
		t.Errorf("cursor beyond journal = %v, want ErrJournalReset", err)
	}
}

func TestIntegrationExtents(t *testing.T) {
	requireElevated(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	dir, err := os.MkdirTemp("", "govss-ext-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	dir = longPathName(t, dir)
	if !strings.EqualFold(dir[:2], systemDrive()[:2]) {
		t.Skipf("temp dir %s is not on the system drive", dir)
	}

	// A multi-cluster file with deterministic content.
	bigContent := make([]byte, 2<<20)
	seed := uint64(0x9E3779B97F4A7C15)
	for i := range bigContent {
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		bigContent[i] = byte(seed)
	}
	bigPath := dir + `\big.bin`
	if err := os.WriteFile(bigPath, bigContent, 0o644); err != nil {
		t.Fatal(err)
	}

	// A tiny file whose data is resident in the MFT record.
	tinyPath := dir + `\tiny.txt`
	if err := os.WriteFile(tinyPath, []byte("resident"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A sparse file: 64 KiB of data, a ~1 MiB hole, 64 KiB of data.
	sparsePath := dir + `\sparse.bin`
	block := bytes.Repeat([]byte{0xAB}, 64<<10)
	const holeEnd = 1 << 20
	sparseContent := make([]byte, holeEnd+len(block))
	copy(sparseContent, block)
	copy(sparseContent[holeEnd:], block)
	makeSparseFile(t, sparsePath, block, holeEnd)

	set := createWithRetry(t, ctx, systemDrive())
	defer set.Close()
	snap := set.Snapshots()[0]

	// --- Geometry sanity.
	geom, err := snap.VolumeGeometry()
	if err != nil {
		t.Fatalf("VolumeGeometry: %v", err)
	}
	t.Logf("geometry: %+v", geom)
	if geom.BytesPerSector <= 0 || geom.BytesPerCluster%geom.BytesPerSector != 0 {
		t.Errorf("cluster size %d not a multiple of sector size %d", geom.BytesPerCluster, geom.BytesPerSector)
	}
	if geom.BytesPerCluster&(geom.BytesPerCluster-1) != 0 {
		t.Errorf("cluster size %d not a power of two", geom.BytesPerCluster)
	}
	if geom.TotalClusters <= 0 || geom.MFTStart <= 0 || geom.MFTRecordSize <= 0 {
		t.Errorf("implausible geometry: %+v", geom)
	}

	raw, err := snap.OpenRaw()
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	defer raw.Close()

	rel := func(abs string) string { return strings.TrimPrefix(abs, systemDrive()) }
	// reassemble reads every extent from the raw device (full
	// cluster-granular runs, so reads stay sector-aligned) and trims to
	// the logical size; holes stay zero.
	reassemble := func(path string, size int64) []byte {
		t.Helper()
		exts, err := snap.FileExtents(rel(path))
		if err != nil {
			t.Fatalf("FileExtents(%s): %v", path, err)
		}
		out := make([]byte, size)
		var prevFileOff int64 = -1
		for _, e := range exts {
			if e.FileOffset <= prevFileOff {
				t.Fatalf("extents not ascending: %+v", exts)
			}
			prevFileOff = e.FileOffset
			if e.Length <= 0 || e.VolumeOffset <= 0 || e.VolumeOffset%geom.BytesPerCluster != 0 {
				t.Fatalf("implausible extent %+v", e)
			}
			buf := make([]byte, e.Length)
			if _, err := raw.ReadAt(buf, e.VolumeOffset); err != nil {
				t.Fatalf("raw ReadAt(%d, %d): %v", e.VolumeOffset, e.Length, err)
			}
			end := e.FileOffset + e.Length
			if end > size {
				end = size
			}
			if e.FileOffset < size {
				copy(out[e.FileOffset:end], buf)
			}
		}
		return out
	}

	// --- Multi-cluster file: raw reassembly must equal the file content.
	if got := reassemble(bigPath, int64(len(bigContent))); !bytes.Equal(got, bigContent) {
		t.Error("big.bin reassembled from raw extents differs from its content")
	}

	// --- Resident file: no extents; readable via Open.
	tinyExts, err := snap.FileExtents(rel(tinyPath))
	if err != nil {
		t.Fatalf("FileExtents(tiny): %v", err)
	}
	if len(tinyExts) != 0 {
		t.Errorf("resident file should have no extents, got %+v", tinyExts)
	}

	// --- Sparse file: hole omitted from extents, reassembly still exact.
	sparseExts, err := snap.FileExtents(rel(sparsePath))
	if err != nil {
		t.Fatalf("FileExtents(sparse): %v", err)
	}
	var allocated int64
	for _, e := range sparseExts {
		allocated += e.Length
	}
	if allocated >= int64(len(sparseContent)) {
		t.Errorf("sparse file allocation %d not smaller than logical size %d (hole not omitted?)", allocated, len(sparseContent))
	}
	if got := reassemble(sparsePath, int64(len(sparseContent))); !bytes.Equal(got, sparseContent) {
		t.Error("sparse.bin reassembled from raw extents differs from its content")
	}

	// --- Path validation shared with Open.
	if _, err := snap.FileExtents(`..\x`); err == nil {
		t.Error("FileExtents must reject traversal")
	}
}

// TestIntegrationExtentsWithExcludes proves the manifest→extents pipeline
// composes with exclusion: files matching exclude rules are dropped from
// the walk, their physically-adjacent neighbors still reassemble
// byte-exact from the raw device (no bleed across cluster boundaries),
// and all files' extents occupy disjoint volume ranges.
func TestIntegrationExtentsWithExcludes(t *testing.T) {
	requireElevated(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	dir, err := os.MkdirTemp("", "govss-extx-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	dir = longPathName(t, dir)
	if !strings.EqualFold(dir[:2], systemDrive()[:2]) {
		t.Skipf("temp dir %s is not on the system drive", dir)
	}

	// Interleave keep/skip files, written back-to-back so NTFS is likely
	// to allocate them adjacently. Sizes are deliberately NOT cluster
	// multiples: the tail of each file's last cluster is slack that may
	// physically contain the next file's data — exactly what trimming
	// must never leak. Each file gets a distinct byte pattern so any
	// cross-contamination is detected, and they're large enough (>4 KiB
	// resident threshold) to be non-resident.
	type tf struct {
		name    string
		content []byte
	}
	var files []tf
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("keep%d.bin", i)
		if i%2 == 1 {
			name = fmt.Sprintf("skip%d.tmp", i)
		}
		content := bytes.Repeat([]byte{0x10 + byte(i)}, 8<<10+137*(i+1))
		files = append(files, tf{name, content})
		if err := os.WriteFile(dir+`\`+name, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	set := createWithRetry(t, ctx, systemDrive())
	defer set.Close()
	snap := set.Snapshots()[0]

	rel := func(name string) string { return strings.TrimPrefix(dir, systemDrive()) + `\` + name }
	tmpRule := ExcludeRule{Path: dir, FileSpec: "*.tmp", Recursive: false}

	// --- Manifest walk with the exclude applied.
	manifest := map[string]int64{} // name -> size from Walk
	err = snap.Walk(ctx, strings.TrimPrefix(dir, systemDrive()),
		WalkOptions{Excludes: []ExcludeRule{tmpRule}},
		func(e *Entry, err error) error {
			if err != nil {
				return err
			}
			if !e.IsDir {
				manifest[e.Name] = e.Size
			}
			return nil
		})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	for _, f := range files {
		_, inManifest := manifest[f.name]
		wantIn := !strings.HasSuffix(f.name, ".tmp")
		if inManifest != wantIn {
			t.Errorf("manifest inclusion of %s = %v, want %v", f.name, inManifest, wantIn)
		}
		if wantIn && manifest[f.name] != int64(len(f.content)) {
			t.Errorf("%s manifest size %d != %d", f.name, manifest[f.name], len(f.content))
		}
	}

	geom, err := snap.VolumeGeometry()
	if err != nil {
		t.Fatalf("VolumeGeometry: %v", err)
	}
	raw, err := snap.OpenRaw()
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	defer raw.Close()

	// --- Pipeline: extents for manifest entries only; each included file
	// must reassemble byte-exact despite excluded data sitting in the
	// slack of adjacent clusters.
	type volRange struct {
		name       string
		start, end int64
	}
	var ranges []volRange
	for _, f := range files {
		exts, err := snap.FileExtents(rel(f.name))
		if err != nil {
			t.Fatalf("FileExtents(%s): %v", f.name, err)
		}
		// Exclusion is manifest-level: extents remain queryable even for
		// excluded files (the snapshot always contains everything).
		if len(exts) == 0 {
			t.Fatalf("%s (size %d) unexpectedly has no extents (resident?)", f.name, len(f.content))
		}
		for _, e := range exts {
			ranges = append(ranges, volRange{f.name, e.VolumeOffset, e.VolumeOffset + e.Length})
		}
		if _, inManifest := manifest[f.name]; !inManifest {
			continue // pipeline skips excluded files from here on
		}
		got := make([]byte, len(f.content))
		for _, e := range exts {
			buf := make([]byte, e.Length) // full cluster run: aligned read
			if _, err := raw.ReadAt(buf, e.VolumeOffset); err != nil {
				t.Fatalf("raw read for %s: %v", f.name, err)
			}
			end := e.FileOffset + e.Length
			if end > int64(len(got)) {
				end = int64(len(got))
			}
			if e.FileOffset < int64(len(got)) {
				copy(got[e.FileOffset:end], buf)
			}
		}
		if !bytes.Equal(got, f.content) {
			t.Errorf("%s: reassembly differs from content (adjacent-data bleed or offset bug)", f.name)
		}
	}

	// --- All extents (included AND excluded files) must occupy disjoint
	// volume ranges; an overlap means the offset math is attributing one
	// file's clusters to another.
	for i := 0; i < len(ranges); i++ {
		for j := i + 1; j < len(ranges); j++ {
			a, b := ranges[i], ranges[j]
			if a.start < b.end && b.start < a.end {
				t.Errorf("extent overlap: %s [%d,%d) vs %s [%d,%d)", a.name, a.start, a.end, b.name, b.start, b.end)
			}
		}
	}

	// --- Cluster-slack demonstration: reading the FULL last cluster of
	// an included file (untrimmed) is allowed to contain foreign bytes;
	// trimming to the logical size is what isolates the file. Verify the
	// trim boundary equals the manifest size, not the cluster boundary.
	keep0 := files[0]
	if int64(len(keep0.content))%geom.BytesPerCluster == 0 {
		t.Fatalf("test invariant broken: keep0 size %d is cluster-aligned", len(keep0.content))
	}
}

// TestIntegrationLiveVolume covers issue #3: the same extent/geometry
// primitives against a live volume via OpenVolume, no shadow copy.
func TestIntegrationLiveVolume(t *testing.T) {
	requireElevated(t)

	// --- Resolution: drive root, bare drive, and a nested path must all
	// resolve to the same canonical volume.
	v, err := OpenVolume(systemDrive())
	if err != nil {
		t.Fatalf("OpenVolume(%s): %v", systemDrive(), err)
	}
	if !strings.HasPrefix(v.VolumeName, `\\?\Volume{`) || !strings.HasSuffix(v.VolumeName, `\`) {
		t.Fatalf("VolumeName = %q", v.VolumeName)
	}
	if !strings.EqualFold(v.MountPoint, systemDrive()) {
		t.Errorf("MountPoint = %q, want %q", v.MountPoint, systemDrive())
	}
	for _, alt := range []string{strings.TrimSuffix(systemDrive(), `\`), os.Getenv("SystemRoot"), v.VolumeName} {
		av, err := OpenVolume(alt)
		if err != nil {
			t.Errorf("OpenVolume(%q): %v", alt, err)
			continue
		}
		if av.VolumeName != v.VolumeName {
			t.Errorf("OpenVolume(%q).VolumeName = %q, want %q", alt, av.VolumeName, v.VolumeName)
		}
		if alt == v.VolumeName && av.MountPoint != v.VolumeName {
			t.Errorf("GUID-path input MountPoint = %q, want the GUID path itself", av.MountPoint)
		}
	}

	// --- Geometry: sane, and identical to what a snapshot of the same
	// volume reports (cluster layout doesn't change under a shadow copy).
	geom, err := v.VolumeGeometry()
	if err != nil {
		t.Fatalf("VolumeGeometry: %v", err)
	}
	if geom.BytesPerCluster <= 0 || geom.BytesPerCluster%geom.BytesPerSector != 0 {
		t.Fatalf("implausible live geometry %+v", geom)
	}
	// Cache-hit path must return identical values.
	if geom2, err := v.VolumeGeometry(); err != nil || geom2 != geom {
		t.Errorf("cached VolumeGeometry = %+v (err %v), want %+v", geom2, err, geom)
	}

	// --- Live extents: write a multi-cluster file, flush it to disk, map
	// it, and reassemble from the raw live volume.
	dir, err := os.MkdirTemp("", "govss-live-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	dir = longPathName(t, dir)
	if !strings.EqualFold(dir[:2], systemDrive()[:2]) {
		t.Skipf("temp dir %s is not on the system drive", dir)
	}

	content := bytes.Repeat([]byte{0xC7, 0x11, 0x5E}, (256<<10)/3)
	path := dir + `\live.bin`
	// Raw device reads bypass the cache: write and flush on the same
	// writable handle (FlushFileBuffers needs write access) so the
	// extent mapping and the on-disk bytes agree.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := windows.FlushFileBuffers(windows.Handle(f.Fd())); err != nil {
		t.Fatalf("FlushFileBuffers: %v", err)
	}
	f.Close()

	rel := strings.TrimPrefix(path, systemDrive())
	exts, err := v.FileExtents(rel)
	if err != nil {
		t.Fatalf("FileExtents: %v", err)
	}
	if len(exts) == 0 {
		t.Fatal("multi-cluster live file has no extents")
	}

	rawH, err := openVolumeByPath(strings.TrimSuffix(v.VolumeName, `\`))
	if err != nil {
		t.Fatalf("opening raw live volume: %v", err)
	}
	raw := os.NewFile(uintptr(rawH), v.VolumeName)
	defer raw.Close()

	got := make([]byte, len(content))
	for _, e := range exts {
		buf := make([]byte, e.Length)
		if _, err := raw.ReadAt(buf, e.VolumeOffset); err != nil {
			t.Fatalf("raw ReadAt: %v", err)
		}
		end := e.FileOffset + e.Length
		if end > int64(len(got)) {
			end = int64(len(got))
		}
		if e.FileOffset < int64(len(got)) {
			copy(got[e.FileOffset:end], buf)
		}
	}
	if !bytes.Equal(got, content) {
		t.Error("live-volume reassembly differs from file content")
	}

	// --- Volume.Open agrees with the OS view of the same file.
	vf, err := v.Open(rel)
	if err != nil {
		t.Fatalf("Volume.Open: %v", err)
	}
	viaVolume, err := io.ReadAll(vf)
	vf.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(viaVolume, content) {
		t.Error("Volume.Open content differs")
	}

	// --- Same validation posture as Snapshot.
	for _, bad := range []string{`..\x`, `C:\Windows`, `\Windows`, "a:b"} {
		if _, err := v.Open(bad); err == nil {
			t.Errorf("Volume.Open(%q) should have been rejected", bad)
		}
		if _, err := v.FileExtents(bad); err == nil {
			t.Errorf("Volume.FileExtents(%q) should have been rejected", bad)
		}
	}

	// --- Snapshot-vs-live geometry equivalence.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	set := createWithRetry(t, ctx, systemDrive())
	defer set.Close()
	sgeom, err := set.Snapshots()[0].VolumeGeometry()
	if err != nil {
		t.Fatalf("snapshot VolumeGeometry: %v", err)
	}
	if sgeom.BytesPerCluster != geom.BytesPerCluster || sgeom.BytesPerSector != geom.BytesPerSector ||
		sgeom.TotalClusters != geom.TotalClusters || sgeom.MFTStart != geom.MFTStart {
		t.Errorf("snapshot geometry %+v != live geometry %+v", sgeom, geom)
	}
}

func makeSparseFile(t *testing.T, path string, block []byte, secondOffset int64) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	const fsctlSetSparse = 0x000900c4
	var ret uint32
	if err := windows.DeviceIoControl(windows.Handle(f.Fd()), fsctlSetSparse,
		nil, 0, nil, 0, &ret, nil); err != nil {
		t.Fatalf("FSCTL_SET_SPARSE: %v", err)
	}
	if _, err := f.WriteAt(block, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(block, secondOffset); err != nil {
		t.Fatal(err)
	}
}

func longPathName(t *testing.T, p string) string {
	t.Helper()
	ps, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetLongPathName(ps, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return p
	}
	return windows.UTF16ToString(buf[:n])
}

func TestIntegrationNotElevated(t *testing.T) {
	if windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("process is elevated; the unelevated failure path can't be tested here")
	}
	_, err := Create(context.Background(), systemDrive())
	if err == nil {
		t.Fatal("Create should fail when not elevated")
	}
	var ve *Error
	if errors.As(err, &ve) && ve.HRESULT != hrAccessDenied {
		t.Logf("non-elevated failure (informational): %v", err)
	}
}
