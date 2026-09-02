// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import "testing"

func TestValidateVolumeRelativePath(t *testing.T) {
	valid := map[string]string{
		"":                             "",
		".":                            "",
		`Users\bob\file.txt`:           `Users\bob\file.txt`,
		"Users/bob/file.txt":           `Users\bob\file.txt`,
		"Windows/System32/drivers/etc": `Windows\System32\drivers\etc`,
		`pagefile.sys`:                 `pagefile.sys`,
		`dir with spaces\file (1).txt`: `dir with spaces\file (1).txt`,
	}
	for in, want := range valid {
		got, err := validateVolumeRelativePath(in)
		if err != nil {
			t.Errorf("validate(%q) unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("validate(%q) = %q, want %q", in, got, want)
		}
	}

	invalid := []string{
		`..\Windows`,
		`Users\..\..\secret`,
		`a/../b`,
		`\Windows`,
		`/Windows`,
		`C:\Windows`,
		"C:",
		`file.txt:stream`, // ADS syntax
		`a\\b`,            // empty segment
		"a//b",
		"nul\x00byte",
		`.\a`,
		`a\.\b`,
	}
	for _, in := range invalid {
		if _, err := validateVolumeRelativePath(in); err == nil {
			t.Errorf("validate(%q) should have been rejected", in)
		}
	}
}
