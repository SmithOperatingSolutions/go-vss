// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeBackend struct {
	closeCalls int
	closeErr   error
}

func (f *fakeBackend) close() error {
	f.closeCalls++
	return f.closeErr
}

func testSet(backend setBackend) *SnapshotSet {
	return &SnapshotSet{
		ID: "{11111111-2222-3333-4444-555555555555}",
		snaps: []*Snapshot{
			{ID: "{AAAAAAAA-0000-0000-0000-000000000001}", VolumePath: `C:\`},
			{ID: "{AAAAAAAA-0000-0000-0000-000000000002}", VolumePath: `D:\`},
		},
		writers: []WriterStatus{
			{Name: "Healthy Writer", State: WriterStable},
			{Name: "Broken Writer", State: WriterState(9) /* FAILED_AT_FREEZE */},
			{Name: "Errored Writer", State: WriterStable, Failure: &Error{Op: "writer", HRESULT: 0x800423F4}},
		},
		excludes: []ExcludeRule{{Writer: "W", Path: `C:\Temp`, FileSpec: "*.tmp"}},
		backend:  backend,
	}
}

func TestSnapshotSetAccessors(t *testing.T) {
	ss := testSet(&fakeBackend{})

	if got := len(ss.Snapshots()); got != 2 {
		t.Errorf("Snapshots() len = %d, want 2", got)
	}
	if s := ss.ForVolume(`D:\`); s == nil || s.ID != "{AAAAAAAA-0000-0000-0000-000000000002}" {
		t.Errorf("ForVolume(D:\\) = %+v", s)
	}
	if s := ss.ForVolume(`E:\`); s != nil {
		t.Errorf("ForVolume(E:\\) should be nil, got %+v", s)
	}
	if got := len(ss.WriterStatuses()); got != 3 {
		t.Errorf("WriterStatuses() len = %d, want 3", got)
	}

	deg := ss.Degraded()
	if len(deg) != 2 {
		t.Fatalf("Degraded() len = %d, want 2 (one failed state, one failure error)", len(deg))
	}
	names := []string{deg[0].Name, deg[1].Name}
	for _, want := range []string{"Broken Writer", "Errored Writer"} {
		if names[0] != want && names[1] != want {
			t.Errorf("Degraded() = %v, missing %q", names, want)
		}
	}

	rules, err := ss.ExcludeRules()
	if err != nil || len(rules) != 1 {
		t.Errorf("ExcludeRules() = %d rules, err=%v", len(rules), err)
	}
}

func TestSnapshotSetCloseIdempotent(t *testing.T) {
	fb := &fakeBackend{}
	ss := testSet(fb)
	if err := ss.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := ss.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if fb.closeCalls != 1 {
		t.Errorf("backend closed %d times, want exactly 1", fb.closeCalls)
	}
}

func TestSnapshotSetClosePropagatesError(t *testing.T) {
	want := errors.New("backend broke")
	ss := testSet(&fakeBackend{closeErr: want})
	if err := ss.Close(); !errors.Is(err, want) {
		t.Errorf("Close = %v, want %v", err, want)
	}
	// Even after an error the set is closed; a retry must not re-close.
	if err := ss.Close(); err != nil {
		t.Errorf("Close after failed close = %v, want nil", err)
	}
}

func TestVerifyOnClosedSet(t *testing.T) {
	ss := testSet(&fakeBackend{})
	ss.Close()
	err := ss.Verify(context.Background())
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("Verify on closed set = %v, want closed error", err)
	}
}

func TestCreateSetArgumentValidation(t *testing.T) {
	ctx := context.Background()
	if _, err := CreateSet(ctx, nil); err == nil {
		t.Error("CreateSet(no volumes) must fail")
	}
	tooMany := make([]string, 65)
	for i := range tooMany {
		tooMany[i] = `C:\`
	}
	_, err := CreateSet(ctx, tooMany)
	if err == nil || !strings.Contains(err.Error(), "64") {
		t.Errorf("CreateSet(65 volumes) = %v, want 64-volume limit error", err)
	}
}

func TestOptions(t *testing.T) {
	cfg := defaultConfig()
	WithTimeouts(Timeouts{Snapshot: 9 * time.Minute})(&cfg)
	if cfg.timeouts.Snapshot != 9*time.Minute {
		t.Errorf("Snapshot timeout = %v, want 9m", cfg.timeouts.Snapshot)
	}
	// Every field must be overridable.
	all := Timeouts{GatherMetadata: 1 * time.Minute, Prepare: 2 * time.Minute, Snapshot: 3 * time.Minute, Complete: 4 * time.Minute}
	cfgAll := defaultConfig()
	WithTimeouts(all)(&cfgAll)
	if cfgAll.timeouts != all {
		t.Errorf("full override = %+v, want %+v", cfgAll.timeouts, all)
	}
	// Zero fields keep defaults.
	def := defaultTimeouts()
	if cfg.timeouts.Prepare != def.Prepare || cfg.timeouts.GatherMetadata != def.GatherMetadata || cfg.timeouts.Complete != def.Complete {
		t.Errorf("zero timeout fields must keep defaults, got %+v", cfg.timeouts)
	}
	if cfg.noWriters {
		t.Error("noWriters default must be false")
	}
	WithoutWriters()(&cfg)
	if !cfg.noWriters {
		t.Error("WithoutWriters must set noWriters")
	}
}

func TestStateStrings(t *testing.T) {
	if StateCreated.String() != "created" || StateAborted.String() != "aborted" || StateDeleted.String() != "deleted" {
		t.Error("SnapshotState.String() mismatch")
	}
	if !strings.Contains(SnapshotState(3).String(), "3") {
		t.Errorf("unknown state should render its number: %q", SnapshotState(3).String())
	}

	if WriterStable.String() != "stable" {
		t.Errorf("WriterStable = %q", WriterStable.String())
	}
	if got := WriterWaitingForBackupComplete.String(); got != "waiting-for-backup-complete" {
		t.Errorf("WriterWaitingForBackupComplete = %q", got)
	}
	for ws, wantFailed := range map[WriterState]bool{
		WriterStable:    false,
		WriterState(6):  true,
		WriterState(15): true,
		WriterState(16): false, // VSS_WS_COUNT sentinel is not a failure
		WriterUnknown:   false,
	} {
		if ws.Failed() != wantFailed {
			t.Errorf("WriterState(%d).Failed() = %v, want %v", ws, ws.Failed(), wantFailed)
		}
	}
	if !strings.Contains(WriterState(9).String(), "failed") {
		t.Errorf("failed state should say so: %q", WriterState(9).String())
	}
}
