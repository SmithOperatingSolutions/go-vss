// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import "testing"

func TestMatchSpec(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		// Literals, case-insensitive.
		{"pagefile.sys", "pagefile.sys", true},
		{"pagefile.sys", "PAGEFILE.SYS", true},
		{"Pagefile.Sys", "pagefile.sys", true},
		{"pagefile.sys", "pagefile.sy", false},
		{"pagefile.sys", "pagefile.sysx", false},
		{"pagefile.sys", "xpagefile.sys", false},

		// Star.
		{"*", "anything.at.all", true},
		// "*" matches empty per glob semantics; ExcludeRule.Matches guards
		// against empty name components before the spec is consulted.
		{"*", "", true},
		{"*.tmp", "x.tmp", true},
		{"*.tmp", "x.TMP", true},
		{"*.tmp", "archive.tar.tmp", true},
		{"*.tmp", "x.tmpx", false},
		{"*.tmp", "x.tm", false},
		{"*.tmp", "tmp", false},
		{"*.tmp", ".tmp", true},
		{"data*", "data001.bin", true},
		{"data*", "data", true},
		{"data*", "mydata", false},
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "abc", true},
		{"a*b*c", "acb", false},
		{"*usn*", "USN JOURNAL", true},

		// Question mark: exactly one character.
		{"log?.txt", "log1.txt", true},
		{"log?.txt", "logAB.txt", false},
		{"log?.txt", "log.txt", false},
		{"??", "ab", true},
		{"??", "a", false},

		// Star + question mark combined.
		{"*.?mp", "a.tmp", true},
		{"*.?mp", "a.bmp", true},
		{"*.?mp", "a.mp", false},

		// Non-ASCII: rune-wise, not byte-wise.
		{"?.txt", "é.txt", true},
		{"*.txt", "übersicht.TXT", true},

		// Trailing stars collapse.
		{"a**", "abc", true},
		{"**", "x", true},
	}
	for _, c := range cases {
		if got := matchSpec(c.pattern, c.name); got != c.want {
			t.Errorf("matchSpec(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestNormWinPath(t *testing.T) {
	cases := map[string]string{
		`C:\`:                `C:\`,
		`C:\Temp\`:           `C:\Temp`,
		`C:\Temp\\`:          `C:\Temp`,
		`C:/Temp/sub/`:       `C:\Temp\sub`,
		`C:\Temp`:            `C:\Temp`,
		`\`:                  `\`,
		``:                   ``,
		`%SystemRoot%\Temp\`: `%SystemRoot%\Temp`,
	}
	for in, want := range cases {
		if got := normWinPath(in); got != want {
			t.Errorf("normWinPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExcludeRuleMatches(t *testing.T) {
	tmp := ExcludeRule{Path: `C:\Temp`, FileSpec: "*.tmp", Recursive: true}
	tmpFlat := ExcludeRule{Path: `C:\Temp`, FileSpec: "*.tmp", Recursive: false}
	pagefile := ExcludeRule{Path: `C:\`, FileSpec: "pagefile.sys"}
	allUnder := ExcludeRule{Path: `C:\Windows\SoftwareDistribution`, FileSpec: "", Recursive: true}

	cases := []struct {
		name string
		rule ExcludeRule
		path string
		want bool
	}{
		{"recursive direct", tmp, `C:\Temp\a.tmp`, true},
		{"recursive nested", tmp, `C:\Temp\sub\deep\a.tmp`, true},
		{"recursive case-insensitive", tmp, `c:\temp\SUB\A.TMP`, true},
		{"recursive forward slashes", tmp, `C:/Temp/sub/a.tmp`, true},
		{"recursive wrong extension", tmp, `C:\Temp\a.txt`, false},
		{"recursive outside dir", tmp, `C:\Other\a.tmp`, false},
		{"dir boundary not prefix-confused", tmp, `C:\TempX\a.tmp`, false},
		{"parent of rule dir", tmp, `C:\a.tmp`, false},

		{"non-recursive direct", tmpFlat, `C:\Temp\a.tmp`, true},
		{"non-recursive rejects nested", tmpFlat, `C:\Temp\sub\a.tmp`, false},

		{"exact file at root", pagefile, `C:\pagefile.sys`, true},
		{"exact file case-insensitive", pagefile, `C:\PAGEFILE.SYS`, true},
		{"exact file wrong dir non-recursive", pagefile, `C:\Windows\pagefile.sys`, false},
		{"exact file wrong name", pagefile, `C:\pagefile2.sys`, false},

		{"empty filespec means star", allUnder, `C:\Windows\SoftwareDistribution\Download\x.cab`, true},

		{"bare name no dir", tmp, `a.tmp`, false},
		{"directory-only path", tmp, `C:\Temp\`, false},
	}
	for _, c := range cases {
		if got := c.rule.Matches(c.path); got != c.want {
			t.Errorf("%s: rule{%q,%q,rec=%v}.Matches(%q) = %v, want %v",
				c.name, c.rule.Path, c.rule.FileSpec, c.rule.Recursive, c.path, got, c.want)
		}
	}
}

func TestExcludeRuleTrailingSlashInRulePath(t *testing.T) {
	// Writers declare paths both with and without trailing separators;
	// both must scope identically.
	with := ExcludeRule{Path: `C:\Windows\Temp\`, FileSpec: "*", Recursive: false}
	without := ExcludeRule{Path: `C:\Windows\Temp`, FileSpec: "*", Recursive: false}
	target := `C:\Windows\Temp\x.log`
	if !with.Matches(target) || !without.Matches(target) {
		t.Errorf("trailing-slash rule path handled inconsistently: with=%v without=%v",
			with.Matches(target), without.Matches(target))
	}
}

func TestExcludeRuleEmptyPathNeverMatches(t *testing.T) {
	r := ExcludeRule{Path: "", FileSpec: "*"}
	if r.Matches(`C:\anything`) {
		t.Error("empty-path rule must never match (would exclude everything)")
	}
}

func TestExcludeRuleRootRecursive(t *testing.T) {
	// The engine-defined "ignore all *.tmp on the volume" case from the
	// package docs.
	r := ExcludeRule{Path: `C:\`, FileSpec: "*.tmp", Recursive: true}
	for path, want := range map[string]bool{
		`C:\a.tmp`:             true,
		`C:\deep\nested\b.TMP`: true,
		`C:\deep\nested\b.txt`: false,
		`D:\a.tmp`:             false,
	} {
		if got := r.Matches(path); got != want {
			t.Errorf("root rule Matches(%q) = %v, want %v", path, got, want)
		}
	}
}

func BenchmarkExcludeRuleMatches(b *testing.B) {
	r := ExcludeRule{Path: `C:\Windows\Temp`, FileSpec: "*.tmp", Recursive: true}
	path := `C:\Windows\Temp\sub\dir\file.tmp`
	for i := 0; i < b.N; i++ {
		r.Matches(path)
	}
}
