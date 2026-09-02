//go:build !windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import "context"

// Walk enumerates the snapshot for manifest building. Not supported on
// this platform.
func (s *Snapshot) Walk(ctx context.Context, root string, opts WalkOptions, fn WalkFunc) error {
	return ErrUnsupported
}
