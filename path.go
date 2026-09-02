// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"fmt"
	"strings"
)

// validateVolumeRelativePath checks a volume-relative Windows path before it
// is joined onto a shadow copy device object. The `\\?\` prefix disables
// Win32 path normalization, so nothing downstream resolves `..` for us —
// we reject rather than repair, per fail-closed input handling.
//
// Accepts forward or backward slashes. "" and "." mean the volume root.
// Returns the path normalized to backslash separators.
func validateVolumeRelativePath(rel string) (string, error) {
	if rel == "" || rel == "." {
		return "", nil
	}
	if strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("vss: path contains NUL")
	}
	// Colons would smuggle drive letters ("C:...") or alternate data
	// stream syntax past validation.
	if strings.ContainsRune(rel, ':') {
		return "", fmt.Errorf("vss: path %q must be volume-relative (no drive letter or stream syntax)", rel)
	}
	norm := strings.ReplaceAll(rel, "/", `\`)
	if strings.HasPrefix(norm, `\`) {
		return "", fmt.Errorf("vss: path %q must be relative (no leading separator)", rel)
	}
	for _, seg := range strings.Split(strings.TrimSuffix(norm, `\`), `\`) {
		switch seg {
		case "":
			return "", fmt.Errorf("vss: path %q contains an empty segment", rel)
		case ".", "..":
			return "", fmt.Errorf("vss: path %q contains a %q segment", rel, seg)
		}
	}
	return norm, nil
}
