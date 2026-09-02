// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import "strings"

// ExcludeRule is one file-exclusion declaration. VSS writers publish these
// to say "do not back these files up by naive copy" (live database files,
// pagefile-class content, temp files); SnapshotSet.ExcludeRules returns the
// writer-declared set. The zero-value-friendly shape also lets a backup
// engine add its own rules with the same matching semantics, e.g.:
//
//	custom := vss.ExcludeRule{Path: `C:\`, FileSpec: "*.tmp", Recursive: true}
type ExcludeRule struct {
	// Writer is the display name of the writer that declared the rule
	// (empty for engine-defined rules).
	Writer string
	// Path is the directory the rule applies to, with environment
	// variables expanded (e.g. `C:\Windows\Temp`).
	Path string
	// RawPath is the path exactly as the writer declared it, possibly
	// containing environment variables (e.g. `%SystemRoot%\Temp`).
	// Empty for engine-defined rules.
	RawPath string
	// FileSpec is a file name or Windows wildcard pattern matched against
	// the file's name component: `pagefile.sys`, `*.tmp`, `*`. Empty
	// means `*`.
	FileSpec string
	// Recursive extends the rule to all subdirectories of Path.
	Recursive bool
}

// Matches reports whether the rule excludes the given absolute Windows
// path (e.g. `C:\Windows\Temp\x.tmp`). Matching is case-insensitive;
// forward slashes are accepted.
//
// Wildcard semantics are the common `*`/`?` subset. The kernel's full
// FsRtlIsNameInExpression grammar has additional DOS-era metacharacters
// (short-name matching, DOS_DOT/DOS_STAR); writer-declared specs in
// practice use only `*`, `?`, and literal names.
func (r ExcludeRule) Matches(path string) bool {
	full := normWinPath(path)
	i := strings.LastIndexByte(full, '\\')
	if i < 0 {
		return false // need a directory component to scope against
	}
	dir, name := full[:i], full[i+1:]
	if name == "" {
		return false
	}
	if len(dir) == 2 && dir[1] == ':' {
		dir += `\` // "C:" -> "C:\" so it compares equal to a root rule
	}
	rulePath := normWinPath(r.Path)
	if rulePath == "" {
		return false
	}

	du, ru := strings.ToUpper(dir), strings.ToUpper(rulePath)
	inScope := du == ru
	if !inScope && r.Recursive {
		prefix := ru
		if !strings.HasSuffix(prefix, `\`) {
			prefix += `\`
		}
		inScope = strings.HasPrefix(du, prefix)
	}
	if !inScope {
		return false
	}

	// "", "*", and the DOS-era "*.*" all mean "every file" — several
	// writers declare "*.*", and FindFirstFile-style matching treats it
	// as matching dotless names too.
	if specMatchesAll(r.FileSpec) {
		return true
	}
	return matchSpec(r.FileSpec, name)
}

// normWinPath normalizes separators and strips trailing backslashes,
// preserving drive roots (`C:\`).
func normWinPath(p string) string {
	p = strings.ReplaceAll(p, "/", `\`)
	for len(p) > 1 && strings.HasSuffix(p, `\`) {
		if len(p) == 3 && p[1] == ':' {
			break // keep "C:\"
		}
		p = p[:len(p)-1]
	}
	return p
}

// matchSpec matches name against a case-insensitive wildcard pattern where
// `*` matches any run (including empty) and `?` matches exactly one
// character. Iterative with single-star backtracking; runs on runes so
// non-ASCII names count characters, not bytes.
func matchSpec(pattern, name string) bool {
	p := []rune(strings.ToUpper(pattern))
	n := []rune(strings.ToUpper(name))
	pi, ni := 0, 0
	star, mark := -1, 0
	for ni < len(n) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == n[ni]):
			pi++
			ni++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, ni
			pi++
		case star >= 0:
			mark++
			pi, ni = star+1, mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}
