//go:build !windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import "context"

// USNJournal queries the change journal state frozen inside this
// snapshot. Not supported on this platform.
func (s *Snapshot) USNJournal() (USNJournalInfo, error) {
	return USNJournalInfo{}, ErrUnsupported
}

// USNChangesSince reads changes since a persisted cursor. Not supported
// on this platform.
func (s *Snapshot) USNChangesSince(ctx context.Context, cursor USNCursor) ([]USNChange, USNCursor, error) {
	return nil, USNCursor{}, ErrUnsupported
}
