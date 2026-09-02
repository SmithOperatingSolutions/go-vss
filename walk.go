// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"io/fs"
	"strings"
	"time"
)

// Entry is one file or directory observed while walking a snapshot — the
// raw material for a backup manifest.
//
// Everything on the volume appears in a walk: a shadow copy is a complete
// point-in-time image, and writer exclude rules do NOT remove files from
// it. Exclusion is a manifest-level decision, which is why it is applied
// here (see WalkOptions.Excludes) and not hidden inside the snapshot.
type Entry struct {
	// Path is the volume-relative path, backslash-separated
	// (e.g. `Windows\System32\drivers\etc\hosts`).
	Path string
	// Name is the final path component.
	Name string
	// IsDir reports whether this is a directory.
	IsDir bool
	// Size is the file size in bytes (0 for directories).
	Size int64
	// Attributes is the raw FILE_ATTRIBUTE_* bitmask.
	Attributes uint32
	// ReparseTag is the reparse tag (IO_REPARSE_TAG_*) when the entry is
	// a reparse point — symlink, junction, dedup/cloud placeholder — and
	// zero otherwise. Reparse points are reported but never traversed: a
	// junction inside a snapshot can point at the live volume.
	ReparseTag uint32
	// Created, Modified, Accessed are the entry's timestamps (UTC).
	Created  time.Time
	Modified time.Time
	Accessed time.Time
	// Excluded marks entries matched by a WalkOptions.Excludes rule; only
	// emitted when WalkOptions.IncludeExcluded is set. ExcludedBy is the
	// first matching rule.
	Excluded   bool
	ExcludedBy *ExcludeRule
}

// IsReparsePoint reports whether the entry is a symlink, junction, or
// other reparse point.
func (e *Entry) IsReparsePoint() bool {
	const fileAttributeReparsePoint = 0x400
	return e.Attributes&fileAttributeReparsePoint != 0
}

// WalkFunc is called for every entry. On a directory-read failure it is
// called with a partial directory Entry and the error; return nil to
// continue with the rest of the walk or the error to abort. Return SkipDir
// for a directory to not descend into it (for a file, the rest of the
// containing directory is skipped, mirroring fs.WalkDir).
type WalkFunc func(e *Entry, err error) error

// SkipDir is fs.SkipDir, re-exported for WalkFunc.
var SkipDir = fs.SkipDir

// WalkOptions configures Snapshot.Walk.
type WalkOptions struct {
	// Excludes are the rules to apply — typically SnapshotSet.ExcludeRules
	// plus SystemExcludes plus any engine-defined rules. Matching runs
	// against the original-volume absolute path (Root + entry path).
	Excludes []ExcludeRule
	// IncludeExcluded emits excluded entries annotated (Excluded /
	// ExcludedBy set) instead of dropping them. Directories whose entire
	// subtree is excluded are emitted but never descended either way.
	IncludeExcluded bool
	// Root is the original volume root used to form absolute paths for
	// exclude matching (e.g. `C:\`). Defaults to Snapshot.VolumePath.
	// When both are empty — a snapshot discovered via List — exclusion
	// matching is disabled and every entry is emitted unannotated.
	Root string
}

// SystemExcludes returns rules for files whose *contents the kernel
// scrubs from shadow copies* (the FilesNotToSnapshot mechanism): they
// appear in a browsed snapshot with their normal directory entries and
// sizes, but reading them returns undefined data. A manifest that copies
// them stores garbage, so exclude them always.
func SystemExcludes(volumeRoot string) []ExcludeRule {
	root := volumeRoot
	if !strings.HasSuffix(root, `\`) {
		root += `\`
	}
	sys := func(spec string) ExcludeRule {
		return ExcludeRule{Writer: "system", Path: root, FileSpec: spec}
	}
	return []ExcludeRule{
		sys("pagefile.sys"),
		sys("hiberfil.sys"),
		sys("swapfile.sys"),
		sys("DumpStack.log.tmp"),
		// VSS's own bookkeeping; unreadable and meaningless to copy.
		{Writer: "system", Path: root + `System Volume Information`, FileSpec: "*", Recursive: true},
	}
}

// specMatchesAll reports whether a filespec matches every file name.
// "*.*" is DOS-era spelling for "all files" (FindFirstFile("*.*") matches
// dotless names too) and several writers declare it that way.
func specMatchesAll(spec string) bool {
	return spec == "" || spec == "*" || spec == "*.*"
}

// matchExclude returns the first rule matching the absolute path.
func matchExclude(rules []ExcludeRule, absPath string) *ExcludeRule {
	for i := range rules {
		if rules[i].Matches(absPath) {
			return &rules[i]
		}
	}
	return nil
}

// subtreeExcluded returns the first rule that excludes *every* file at or
// below absDir: a recursive match-all rule whose scope covers the
// directory. Walkers use it to prune whole subtrees.
func subtreeExcluded(rules []ExcludeRule, absDir string) *ExcludeRule {
	d := strings.ToUpper(normWinPath(absDir))
	if d == "" {
		return nil
	}
	for i := range rules {
		r := &rules[i]
		if !r.Recursive || !specMatchesAll(r.FileSpec) {
			continue
		}
		scope := strings.ToUpper(normWinPath(r.Path))
		if scope == "" {
			continue
		}
		if d == scope {
			return r
		}
		prefix := scope
		if !strings.HasSuffix(prefix, `\`) {
			prefix += `\`
		}
		if strings.HasPrefix(d, prefix) {
			return r
		}
	}
	return nil
}
