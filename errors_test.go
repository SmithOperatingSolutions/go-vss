// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestErrorMessage(t *testing.T) {
	e := &Error{Op: "DoSnapshotSet", HRESULT: 0x80042316}
	msg := e.Error()
	for _, want := range []string{"DoSnapshotSet", "VSS_E_SNAPSHOT_SET_IN_PROGRESS", "0x80042316", "retry"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q missing %q", msg, want)
		}
	}

	unknown := &Error{Op: "X", HRESULT: 0x80099999}
	if !strings.Contains(unknown.Error(), "0x80099999") {
		t.Errorf("unknown HRESULT should still render the code: %q", unknown.Error())
	}
}

func TestRetryable(t *testing.T) {
	cases := []struct {
		hr   uint32
		want bool
	}{
		{hrSnapshotSetInProgress, true},
		{hrFlushWritesTimeout, true},
		{hrHoldWritesTimeout, true},
		{hrWriterErrorRetryable, true},
		{hrWriterErrorTimeout, true},
		{hrAccessDenied, false},
		{hrBadState, false},
		{0x800423F4, false}, // NONRETRYABLE
	}
	for _, c := range cases {
		e := &Error{Op: "op", HRESULT: c.hr}
		if got := e.Retryable(); got != c.want {
			t.Errorf("Retryable(0x%08X) = %v, want %v", c.hr, got, c.want)
		}
		wrapped := fmt.Errorf("outer: %w", e)
		if got := IsRetryable(wrapped); got != c.want {
			t.Errorf("IsRetryable(wrapped 0x%08X) = %v, want %v", c.hr, got, c.want)
		}
	}
	if IsRetryable(errors.New("plain")) {
		t.Error("plain error must not be retryable")
	}
}

func TestHRErrTruncatesSignExtension(t *testing.T) {
	// On 64-bit, SyscallN may sign-extend a failure HRESULT. hrErr must
	// compare the truncated 32-bit value. (Computed, not a constant, so
	// this also compiles where uintptr is 32 bits.)
	raw := uint32(0x80042301)
	signExtended := uintptr(int(int32(raw)))
	err := hrErr(signExtended, "op")
	var ve *Error
	if !errors.As(err, &ve) || ve.HRESULT != 0x80042301 {
		t.Fatalf("hrErr(sign-extended) = %v, want VSS_E_BAD_STATE", err)
	}
}

func TestHRErrSuccessCodes(t *testing.T) {
	for _, hr := range []uintptr{hrOK, hrSFalse, hrAsyncFinished} {
		if err := hrErr(hr, "op"); err != nil {
			t.Errorf("hrErr(0x%X) = %v, want nil", hr, err)
		}
	}
}

func TestErrUnsupportedWrapsStdlib(t *testing.T) {
	if !errors.Is(ErrUnsupported, errors.ErrUnsupported) {
		t.Error("ErrUnsupported must wrap errors.ErrUnsupported")
	}
}
