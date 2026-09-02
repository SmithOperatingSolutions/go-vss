// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"testing"
)

func TestSpecMatchesAll(t *testing.T) {
	for spec, want := range map[string]bool{
		"":      true,
		"*":     true,
		"*.*":   true,
		"*.tmp": false,
		"a*":    false,
		"?":     false,
		"* *":   false,
	} {
		if got := specMatchesAll(spec); got != want {
			t.Errorf("specMatchesAll(%q) = %v, want %v", spec, got, want)
		}
	}
}

func TestMatchesTreatsDosStarDotStarAsAll(t *testing.T) {
	// FindFirstFile("*.*") matches dotless names; writers (MSSearch)
	// declare "*.*" with that meaning.
	r := ExcludeRule{Path: `C:\Data`, FileSpec: "*.*"}
	if !r.Matches(`C:\Data\noextension`) {
		t.Error(`"*.*" must match dotless file names`)
	}
	if !r.Matches(`C:\Data\file.txt`) {
		t.Error(`"*.*" must match dotted file names`)
	}
}

func TestMatchExcludeFirstWins(t *testing.T) {
	rules := []ExcludeRule{
		{Writer: "first", Path: `C:\Temp`, FileSpec: "*.tmp"},
		{Writer: "second", Path: `C:\Temp`, FileSpec: "*"},
	}
	hit := matchExclude(rules, `C:\Temp\a.tmp`)
	if hit == nil || hit.Writer != "first" {
		t.Errorf("matchExclude = %+v, want first rule", hit)
	}
	if hit := matchExclude(rules, `C:\Other\a.tmp`); hit != nil {
		t.Errorf("matchExclude out of scope = %+v, want nil", hit)
	}
	if hit := matchExclude(nil, `C:\Temp\a.tmp`); hit != nil {
		t.Errorf("matchExclude with no rules = %+v, want nil", hit)
	}
}

func TestSubtreeExcluded(t *testing.T) {
	rules := []ExcludeRule{
		{Writer: "files-only", Path: `C:\Logs`, FileSpec: "*.log", Recursive: true},
		{Writer: "flat", Path: `C:\Flat`, FileSpec: "*", Recursive: false},
		{Writer: "svi", Path: `C:\System Volume Information`, FileSpec: "*", Recursive: true},
		{Writer: "dotstar", Path: `C:\Search`, FileSpec: "*.*", Recursive: true},
	}
	cases := []struct {
		dir  string
		want string // writer of expected rule, "" for none
	}{
		{`C:\System Volume Information`, "svi"},
		{`C:\System Volume Information\sub\deep`, "svi"},
		{`c:\system volume information\SUB`, "svi"},
		{`C:\Search\any\where`, "dotstar"},
		// A file-pattern rule never excludes a whole subtree.
		{`C:\Logs`, ""},
		{`C:\Logs\sub`, ""},
		// A non-recursive match-all rule never excludes a subtree.
		{`C:\Flat`, ""},
		{`C:\Flat\sub`, ""},
		// Prefix confusion.
		{`C:\System Volume Informationx`, ""},
		{`C:\`, ""},
	}
	for _, c := range cases {
		got := subtreeExcluded(rules, c.dir)
		gotWriter := ""
		if got != nil {
			gotWriter = got.Writer
		}
		if gotWriter != c.want {
			t.Errorf("subtreeExcluded(%q) = %q, want %q", c.dir, gotWriter, c.want)
		}
	}
	if subtreeExcluded(rules, "") != nil {
		t.Error("empty dir must not be subtree-excluded")
	}
}

func TestSystemExcludes(t *testing.T) {
	for _, root := range []string{`C:\`, `C:`} {
		rules := SystemExcludes(root)
		mustExclude := []string{
			`C:\pagefile.sys`,
			`C:\hiberfil.sys`,
			`C:\swapfile.sys`,
			`C:\DumpStack.log.tmp`,
			`C:\System Volume Information\anything`,
			`C:\System Volume Information\sub\deep.file`,
		}
		for _, p := range mustExclude {
			if matchExclude(rules, p) == nil {
				t.Errorf("SystemExcludes(%q) must exclude %s", root, p)
			}
		}
		mustKeep := []string{
			`C:\Users\bob\pagefile.sys.txt`,
			`C:\Users\bob\document.docx`,
			`C:\Windows\System32\ntdll.dll`,
			// pagefile on another volume is that volume's problem
			`D:\pagefile.sys`,
		}
		for _, p := range mustKeep {
			if r := matchExclude(rules, p); r != nil {
				t.Errorf("SystemExcludes(%q) wrongly excludes %s (rule %+v)", root, p, r)
			}
		}
		// The scrubbed files are root-scoped, non-recursive: a stray copy
		// deeper in the tree is real data.
		if matchExclude(rules, `C:\backup\pagefile.sys`) != nil {
			t.Errorf("root-scoped pagefile rule must not match nested copies")
		}
		if subtreeExcluded(rules, `C:\System Volume Information\x`) == nil {
			t.Errorf("SVI subtree must be prunable")
		}
	}
}

func TestEntryIsReparsePoint(t *testing.T) {
	e := Entry{Attributes: 0x400}
	if !e.IsReparsePoint() {
		t.Error("attribute 0x400 must report reparse point")
	}
	if (&Entry{Attributes: 0x20}).IsReparsePoint() {
		t.Error("archive attribute must not report reparse point")
	}
}
