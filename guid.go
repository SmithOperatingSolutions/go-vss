// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"fmt"
	"strings"
)

// guid is a Windows GUID with the same memory layout as windows.GUID /
// the C GUID struct. Defined here (not via x/sys) so GUID handling is
// testable on every platform.
type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var guidNull = guid{}

// String formats as "{XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX}" (uppercase,
// braced — the form vssadmin and the Windows tooling use).
func (g guid) String() string {
	return fmt.Sprintf("{%08X-%04X-%04X-%02X%02X-%02X%02X%02X%02X%02X%02X}",
		g.Data1, g.Data2, g.Data3,
		g.Data4[0], g.Data4[1], g.Data4[2], g.Data4[3],
		g.Data4[4], g.Data4[5], g.Data4[6], g.Data4[7])
}

// parseGUID accepts braced or bare, upper or lower case.
func parseGUID(s string) (guid, error) {
	s = strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(s), "}"), "{")
	var g guid
	var d [8]byte
	n, err := fmt.Sscanf(s, "%08x-%04x-%04x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		&g.Data1, &g.Data2, &g.Data3,
		&d[0], &d[1], &d[2], &d[3], &d[4], &d[5], &d[6], &d[7])
	if err != nil || n != 11 {
		return guid{}, fmt.Errorf("vss: invalid GUID %q", s)
	}
	g.Data4 = d
	return g, nil
}
