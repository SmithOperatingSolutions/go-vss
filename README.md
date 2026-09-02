# go-vss

Pure-Go (no cgo) Windows **Volume Shadow Copy Service** requester: create,
list, read, and delete VSS snapshots from a backup engine — with the OS and
access details abstracted away.

```go
import vss "github.com/SmithOperatingSolutions/go-vss"

set, err := vss.Create(ctx, `C:\`)
if err != nil { ... }
defer set.Close() // notifies writers, deletes the snapshot

snap := set.Snapshots()[0]

// Read through a standard fs.FS (forward-slash paths, works with
// fs.WalkDir / fs.ReadFile / fs.ReadDir):
data, err := fs.ReadFile(snap.FS(), "Users/bob/Documents/ledger.db")

// Or by volume-relative Windows path:
f, err := snap.Open(`Users\bob\Documents\ledger.db`)

// Or the raw block device, for image-level backup:
dev, err := snap.OpenRaw()
```

The package compiles on every platform; on non-Windows (and on 32-bit or
WOW64/emulated Windows processes) all operations return `vss.ErrUnsupported`,
which wraps `errors.ErrUnsupported`.

## What it does

- **Snapshot** one volume or up to 64 volumes atomically in one shadow copy
  set (`Create` / `CreateSet`), via the real COM requester API
  (`IVssBackupComponents`), not `vssadmin`/`wmic` output parsing.
- **File-consistent by default**: writers participate and quiesce
  (`VSS_CTX_BACKUP`, `VSS_BT_COPY` — never triggers SQL/Exchange log
  truncation). `WithoutWriters()` gives crash-consistent snapshots with no
  writer dependency.
- **Read** through `fs.FS`, volume-relative `Open`, or the raw device.
  Files are opened with backup semantics (DACL bypass via
  `SeBackupPrivilege`) and reparse points are **never followed** — a
  junction inside a snapshot can point at the live volume.
- **List** all shadow copies on the system (`List`), across all contexts.
- **Detect degraded writers** (`SnapshotSet.Degraded`) and **detect
  snapshots deleted under you** mid-backup (`SnapshotSet.Verify`) — the
  diff-area-exhaustion failure mode that silently truncates backups.
- **Writer exclude-file lists** (`SnapshotSet.ExcludeRules`): the files
  writers declare must not be backed up by naive copy (live databases,
  pagefile-class content). Each rule is a directory + wildcard filespec +
  recursive flag with env vars expanded; `ExcludeRule.Matches` implements
  the (case-insensitive, `*`/`?`) matching, and engines can add their own
  rules with the same type:

  ```go
  rules, _ := set.ExcludeRules() // writer-declared
  rules = append(rules, vss.ExcludeRule{Path: `C:\`, FileSpec: "*.tmp", Recursive: true})
  for _, r := range rules {
      if r.Matches(`C:\Windows\Temp\x.tmp`) { /* skip it */ }
  }
  ```
- **Fail closed everywhere**: bounded async waits (a wedged writer produces
  a timeout, not a hang), validated-and-rejected paths (no `..`, drive
  letters, ADS syntax), `AbortBackup` on every error path, explicit
  privilege-enable verification.

## Manifest walking, and what a browsed snapshot contains

`Snapshot.Walk` enumerates the snapshot for manifest building: rich Windows
metadata (size, attributes, all timestamps, reparse tags) straight from the
directory enumeration (no per-file opens), deterministic ordering so two
walks produce identical manifests, `SkipDir` pruning, and per-directory
error callbacks so one unreadable directory doesn't kill a volume walk.

```go
rules, _ := set.ExcludeRules()
rules = append(rules, vss.SystemExcludes(snap.VolumePath)...)
err := snap.Walk(ctx, "", vss.WalkOptions{Excludes: rules}, func(e *vss.Entry, err error) error {
    if err != nil { log(...); return nil }        // unreadable dir: continue
    if e.IsReparsePoint() { record link; return nil } // reported, never followed
    manifest.Add(e.Path, e.Size, e.Modified)
    return nil
})
```

Three facts about snapshot contents worth internalizing:

1. **Exclude rules don't change the snapshot.** A shadow copy is a complete
   point-in-time image; every file is present and readable when you browse
   it, including everything writers told you to exclude. Exclusion is a
   manifest decision — which is why `Walk` applies it and `FS()`/`Open`
   don't.
2. **A few files are scrubbed by the kernel.** `pagefile.sys`,
   `hiberfil.sys`, `swapfile.sys` and the `FilesNotToSnapshot` registry set
   appear in listings with their full size, but reading them from a
   snapshot returns undefined data. `vss.SystemExcludes(root)` covers them —
   always include it, or your manifest faithfully copies garbage.
3. **Reparse points are the escape hatch to guard.** A junction inside the
   snapshot can target the live volume; `Walk` and `Open` report them
   (`Entry.ReparseTag`) but never traverse them.

Ordering note for backup engines: the full `Walk` manifest is the baseline;
USN-journal incremental change detection diffs against it later. Full scan
comes first.

## Incremental backups (USN change journal)

The incremental cursor is **captured from the live volume just before the
freeze** (`Snapshot.USNCursor`) — never from a journal query against the
shadow device, whose `NextUSN` gets rounded up during the snapshot's
crash-consistent mount recovery and would silently skip changes. Records
are read from the live journal (NTFS does not serve journal reads from
shadow copies) bounded by that captured position; paths and deletions are
resolved inside the snapshot. Files touched in the tiny capture-to-freeze
window are re-reported next run — safe over-reporting, never
under-reporting. The protocol:

```go
// Backup N: after a successful backup, persist the snapshot's cursor.
saveCursor(snap.USNCursor) // {journal_id, next_usn}, JSON-friendly

// Backup N+1: snapshot first, then diff.
changes, next, err := snap.USNChangesSince(ctx, loadCursor())
switch {
case errors.Is(err, vss.ErrJournalReset), errors.Is(err, vss.ErrTooManyChanges):
    fullWalk(snap) // fail closed: never treat an unusable journal as "no changes"
case err != nil:
    return err
default:
    for _, c := range changes {
        // c.Path (volume-relative, resolved inside the snapshot),
        // c.Deleted (gone at this point in time), c.Reasons, c.FileID
    }
    saveCursor(next)
}
```

Correctness properties, all enforced or surfaced by the API:

- **Fail closed**: journal recreated, wrapped past the cursor, cursor from
  another volume, purged entries, ReFS V3 records, or >1M changed files
  all return `ErrJournalReset`/`ErrTooManyChanges` — the answer is a full
  walk, never an empty incremental.
- **Deletes are relative to the snapshot**: `Deleted` means the file ID no
  longer resolves in the snapshot being read; its last-known path is
  reconstructed via the parent directory.
- **Directory renames** produce records only for the directory itself — the
  paths of everything beneath changed without records. Match manifests by
  `FileID`, or re-walk a renamed directory's subtree (documented on
  `USNChange`).
- NTFS only for now (V2 records); ReFS volumes fail closed to full scans.

## Requirements

| Requirement | Detail |
|---|---|
| OS | Windows 7 / Server 2008 R2 through Windows 11 / Server 2025 |
| Architecture | native amd64 or arm64; 32-bit and WOW64/emulated processes are refused at startup |
| Privilege | elevated process (Administrators); `SeBackupPrivilege` is enabled automatically and required |
| Source filesystems | NTFS fully (extents/geometry/USN). FAT32 — including unmounted/letterless volumes by `\\?\Volume{GUID}\` path, i.e. the EFI System Partition shape — is **effectively not snapshot-able** (CI-verified on both architectures): out-of-the-box `CreateSet` fails `VSS_E_INSUFFICIENT_STORAGE` (the diff area defaults to the source volume and shadow *storage* must be NTFS), and Windows refuses to create a shadow-storage association for a FAT32 source (`Win32_ShadowStorage.Create` returns volume-not-supported / provider-veto on Server 2022 / Windows 11). Capture the ESP via raw ranged disk reads instead — it is small and quiescent. ReFS snapshots work but USN incrementals fail closed to full scans |

## Design notes

- VSS is a C++ vtable COM API with no type library; methods are invoked by
  vtable slot index with a hand-built MSVC ABI. Slot order follows the
  `vsbackup.h` declaration order and is verified at init against the
  documented slot count — a mismatch is a startup panic, not silent
  memory corruption.
- 16-byte GUIDs are passed by value with **per-architecture call shapes**
  (by-reference on amd64, two registers on arm64 — see
  `guidargs_*.go`). The arm64 shape is additionally validated at runtime:
  snapshot creation refuses to return unless the device path parses as a
  `\\?\GLOBALROOT\Device\...` path, which an ABI mismatch cannot produce.
- All COM calls run on one dedicated, OS-locked thread per session with an
  MTA apartment (goroutines migrate threads; COM apartment state is
  per-thread). Reads of snapshot data are plain Win32 and safe from any
  goroutine.
- One `IVssBackupComponents` object per operation, per the VSS lifecycle
  contract; `vssapi.dll` is loaded with System32-only search semantics.

The API rationale — every vtable slot, HRESULT, ABI hazard, and lifecycle
rule — is documented inline at each type and method in the source.

## Testing

- `go test ./...` runs unit tests on any platform (GUID/HRESULT/path
  handling, layout assertions and export resolution on Windows).
- On an **elevated** Windows process the integration tests also run: they
  create a real snapshot of the system drive, read files and raw blocks
  from it, list and verify it, and confirm deletion on `Close`. They skip
  automatically when not elevated.
- CI (`.github/workflows/test.yml`) runs Linux unit + cross-compile checks,
  and the full integration suite on `windows-latest` (amd64) and
  `windows-11-arm` (arm64) — GitHub-hosted Windows runners are elevated.
- `cmd/vssdemo` is a manual smoke tool: `vssdemo snap C:\`,
  `vssdemo read C:\ Windows\System32\drivers\etc\hosts`, `vssdemo list`,
  `vssdemo hold C:\ 10m`.

## License

Apache-2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).
