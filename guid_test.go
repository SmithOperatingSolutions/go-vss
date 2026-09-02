// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import "testing"

func TestGUIDRoundTrip(t *testing.T) {
	cases := []string{
		"{665C1D5F-C218-414D-A05D-7FEF5F9D5C86}",
		"{00000000-0000-0000-0000-000000000000}",
		"{B5946137-7B9F-4925-AF80-51ABD60B20D5}",
	}
	for _, s := range cases {
		g, err := parseGUID(s)
		if err != nil {
			t.Fatalf("parseGUID(%q): %v", s, err)
		}
		if got := g.String(); got != s {
			t.Errorf("round trip %q -> %q", s, got)
		}
	}
}

func TestParseGUIDForms(t *testing.T) {
	want := guid{0x665c1d5f, 0xc218, 0x414d, [8]byte{0xa0, 0x5d, 0x7f, 0xef, 0x5f, 0x9d, 0x5c, 0x86}}
	for _, s := range []string{
		"{665c1d5f-c218-414d-a05d-7fef5f9d5c86}", // lowercase braced
		"665C1D5F-C218-414D-A05D-7FEF5F9D5C86",   // bare
	} {
		g, err := parseGUID(s)
		if err != nil {
			t.Fatalf("parseGUID(%q): %v", s, err)
		}
		if g != want {
			t.Errorf("parseGUID(%q) = %+v, want %+v", s, g, want)
		}
	}
}

func TestParseGUIDRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "not-a-guid", "{665C1D5F}", "{665C1D5F-C218-414D-A05D}"} {
		if _, err := parseGUID(s); err == nil {
			t.Errorf("parseGUID(%q) should fail", s)
		}
	}
}

func TestGUIDNullIsZero(t *testing.T) {
	if guidNull.String() != "{00000000-0000-0000-0000-000000000000}" {
		t.Errorf("guidNull = %s", guidNull.String())
	}
}
