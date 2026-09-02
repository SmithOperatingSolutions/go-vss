//go:build !windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"context"
	"errors"
	"io/fs"
	"testing"
)

func TestUnsupportedPlatform(t *testing.T) {
	ctx := context.Background()

	if _, err := Create(ctx, "/"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Create = %v, want ErrUnsupported", err)
	}
	if _, err := List(ctx); !errors.Is(err, ErrUnsupported) {
		t.Errorf("List = %v, want ErrUnsupported", err)
	}

	var s Snapshot
	if _, err := s.Open("x"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Open = %v, want ErrUnsupported", err)
	}
	if _, err := s.OpenRaw(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("OpenRaw = %v, want ErrUnsupported", err)
	}
	if _, err := s.FS().Open("x"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("FS().Open = %v, want ErrUnsupported", err)
	}
	err := s.Walk(ctx, "", WalkOptions{}, func(e *Entry, err error) error { return nil })
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("Walk = %v, want ErrUnsupported", err)
	}
	if _, err := s.VolumeGeometry(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("VolumeGeometry = %v, want ErrUnsupported", err)
	}
	if _, err := s.FileExtents("x"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("FileExtents = %v, want ErrUnsupported", err)
	}
	if _, err := OpenVolume("/"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("OpenVolume = %v, want ErrUnsupported", err)
	}
	var v Volume
	if _, err := v.VolumeGeometry(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Volume.VolumeGeometry = %v, want ErrUnsupported", err)
	}
	if _, err := v.FileExtents("x"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Volume.FileExtents = %v, want ErrUnsupported", err)
	}
	if _, err := v.Open("x"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Volume.Open = %v, want ErrUnsupported", err)
	}
	if _, err := s.USNJournal(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("USNJournal = %v, want ErrUnsupported", err)
	}
	if _, _, err := s.USNChangesSince(ctx, USNCursor{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("USNChangesSince = %v, want ErrUnsupported", err)
	}
	if _, err := EnumerateVolumes(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("EnumerateVolumes = %v, want ErrUnsupported", err)
	}
	if _, err := v.DiskExtents(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("DiskExtents = %v, want ErrUnsupported", err)
	}
	if _, err := Attach(ctx, "{11111111-2222-3333-4444-555555555555}"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Attach = %v, want ErrUnsupported", err)
	}
	if err := Delete(ctx, "{11111111-2222-3333-4444-555555555555}"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Delete = %v, want ErrUnsupported", err)
	}
	// Malformed IDs are rejected before any platform work.
	if _, err := Attach(ctx, "not-a-guid"); err == nil || errors.Is(err, ErrUnsupported) {
		t.Errorf("Attach(bad id) = %v, want parse error", err)
	}
	if err := Delete(ctx, "not-a-guid"); err == nil || errors.Is(err, ErrUnsupported) {
		t.Errorf("Delete(bad id) = %v, want parse error", err)
	}
	// The error FS must still return proper PathErrors for fs.FS clients.
	if _, err := fs.ReadFile(s.FS(), "x"); err == nil {
		t.Error("fs.ReadFile should fail on unsupported platform")
	}
}

func TestVerifyPropagatesListFailure(t *testing.T) {
	// On unsupported platforms List fails; Verify on an open set must
	// surface that instead of pretending the snapshots are fine.
	ss := testSet(&fakeBackend{})
	if err := ss.Verify(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Verify = %v, want wrapped ErrUnsupported", err)
	}
}
