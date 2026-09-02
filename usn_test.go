// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestUSNCursorJSONRoundTrip(t *testing.T) {
	// Engines persist cursors between runs; the JSON shape is API.
	c := USNCursor{JournalID: 0xDEADBEEF12345678, NextUSN: 987654321}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	// The field names are API (persisted state); pin them.
	for _, key := range []string{`"journal_id":`, `"next_usn":987654321`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("cursor JSON %s missing %s", b, key)
		}
	}
	var back USNCursor
	if err := json.Unmarshal(b, &back); err != nil || back != c {
		t.Errorf("round trip = %+v (err %v), want %+v", back, err, c)
	}
}

func TestUSNJournalInfoCursor(t *testing.T) {
	info := USNJournalInfo{ID: 7, FirstUSN: 100, NextUSN: 5000}
	c := info.Cursor()
	if c.JournalID != 7 || c.NextUSN != 5000 {
		t.Errorf("Cursor() = %+v", c)
	}
}

func TestUSNSentinelsAreDistinct(t *testing.T) {
	if errors.Is(ErrJournalReset, ErrTooManyChanges) || errors.Is(ErrTooManyChanges, ErrJournalReset) {
		t.Error("ErrJournalReset and ErrTooManyChanges must be distinct")
	}
}
