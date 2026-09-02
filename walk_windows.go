//go:build windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// Walk enumerates the snapshot beneath root (a volume-relative path; "" or
// "." for the whole volume) and calls fn for every entry, directories
// before their contents, siblings in a deterministic case-insensitive
// order — so two walks of the same snapshot produce identical manifests.
//
// Semantics that matter for building a backup manifest:
//
//   - Everything on the volume is visible: exclude rules do not alter the
//     snapshot, they only annotate/drop entries per WalkOptions.
//   - Reparse points (symlinks, junctions) are reported with their tag but
//     never traversed — following one could silently escape the snapshot
//     onto the live volume.
//   - Directories wholly covered by a recursive match-all exclude rule are
//     pruned without descending.
//   - Unreadable directories invoke fn with the error; fn decides whether
//     to continue (return nil) or abort.
func (s *Snapshot) Walk(ctx context.Context, root string, opts WalkOptions, fn WalkFunc) error {
	if s.DeviceObject == "" {
		return fmt.Errorf("vss: snapshot has no device object")
	}
	rootRel, err := validateVolumeRelativePath(root)
	if err != nil {
		return err
	}
	volRoot := opts.Root
	if volRoot == "" {
		volRoot = s.VolumePath
	}
	if volRoot != "" && !strings.HasSuffix(volRoot, `\`) {
		volRoot += `\`
	}
	w := &walker{
		dev:     s.DeviceObject,
		volRoot: volRoot,
		opts:    opts,
		fn:      fn,
	}
	err = w.walkDir(ctx, rootRel)
	if errors.Is(err, SkipDir) {
		return nil
	}
	return err
}

type walker struct {
	dev     string
	volRoot string
	opts    WalkOptions
	fn      WalkFunc
}

// abs forms the original-volume absolute path for exclude matching, or ""
// when no volume root is known (matching disabled).
func (w *walker) abs(rel string) string {
	if w.volRoot == "" {
		return ""
	}
	return w.volRoot + rel
}

func (w *walker) walkDir(ctx context.Context, rel string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	entries, err := w.readDir(rel)
	if err != nil {
		// Report and let the callback decide; a single unreadable
		// directory (ACLs, transient) shouldn't kill a volume walk unless
		// the engine wants it to.
		return w.fn(&Entry{Path: rel, Name: baseName(rel), IsDir: true}, err)
	}

	for i := range entries {
		e := &entries[i]
		if rel != "" {
			e.Path = rel + `\` + e.Name
		} else {
			e.Path = e.Name
		}
		abs := w.abs(e.Path)

		descend := e.IsDir && !e.IsReparsePoint()
		if abs != "" && len(w.opts.Excludes) > 0 {
			var hit *ExcludeRule
			if descend {
				// Only a recursive match-all rule excludes a directory
				// (and with it the whole subtree). A file-pattern rule
				// like *.tmp matching a directory's *name* does not
				// exclude the directory's contents.
				hit = subtreeExcluded(w.opts.Excludes, abs)
			} else if !e.IsDir || e.IsReparsePoint() {
				hit = matchExclude(w.opts.Excludes, abs)
			}
			if hit != nil {
				if !w.opts.IncludeExcluded {
					continue
				}
				e.Excluded = true
				e.ExcludedBy = hit
				descend = false // wholly-excluded subtrees are never entered
			}
		}

		switch err := w.fn(e, nil); {
		case err == nil:
		case errors.Is(err, SkipDir):
			if e.IsDir {
				continue // don't descend, keep walking siblings
			}
			return nil // stdlib semantics: skip the rest of this directory
		default:
			return err
		}

		if descend && !e.Excluded {
			if err := w.walkDir(ctx, e.Path); err != nil {
				return err
			}
		}
	}
	return nil
}

// readDir enumerates one directory of the snapshot, sorted for
// deterministic manifests. Uses FindFirstFile directly on the GLOBALROOT
// device path: no Go path cleaning, long-path safe, and the enumeration
// data already carries sizes/times/attributes so no per-file opens.
func (w *walker) readDir(rel string) ([]Entry, error) {
	pattern := w.dev + `\` + `*`
	if rel != "" {
		pattern = w.dev + `\` + rel + `\*`
	}
	patp, err := utf16Ptr(pattern)
	if err != nil {
		return nil, err
	}
	var fd windows.Win32finddata
	h, err := windows.FindFirstFile(patp, &fd)
	if err != nil {
		return nil, fmt.Errorf("enumerating %q: %w", rel, err)
	}
	defer windows.FindClose(h)

	var out []Entry
	for {
		name := windows.UTF16ToString(fd.FileName[:])
		if name != "." && name != ".." {
			out = append(out, entryFromFindData(name, &fd))
		}
		if err := windows.FindNextFile(h, &fd); err != nil {
			if err == windows.ERROR_NO_MORE_FILES {
				break
			}
			return nil, fmt.Errorf("enumerating %q: %w", rel, err)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToUpper(out[i].Name), strings.ToUpper(out[j].Name)
		if a == b {
			return out[i].Name < out[j].Name
		}
		return a < b
	})
	return out, nil
}

func entryFromFindData(name string, fd *windows.Win32finddata) Entry {
	const fileAttributeDirectory = 0x10
	e := Entry{
		Name:       name,
		IsDir:      fd.FileAttributes&fileAttributeDirectory != 0,
		Attributes: fd.FileAttributes,
		Created:    time.Unix(0, fd.CreationTime.Nanoseconds()).UTC(),
		Modified:   time.Unix(0, fd.LastWriteTime.Nanoseconds()).UTC(),
		Accessed:   time.Unix(0, fd.LastAccessTime.Nanoseconds()).UTC(),
	}
	if !e.IsDir {
		e.Size = int64(fd.FileSizeHigh)<<32 | int64(fd.FileSizeLow)
	}
	if e.IsReparsePoint() {
		// Reserved0 holds the reparse tag only for reparse points.
		e.ReparseTag = fd.Reserved0
	}
	return e
}

func baseName(rel string) string {
	if i := strings.LastIndexByte(rel, '\\'); i >= 0 {
		return rel[i+1:]
	}
	return rel
}
