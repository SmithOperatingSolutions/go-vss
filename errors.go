// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"errors"
	"fmt"
)

// ErrUnsupported is returned on non-Windows platforms and on unsupported
// architectures (32-bit builds, or any binary running under WOW64
// emulation). It wraps errors.ErrUnsupported.
var ErrUnsupported = fmt.Errorf("vss: volume shadow copies require a native 64-bit Windows process: %w", errors.ErrUnsupported)

// Error is a failed VSS or COM call. HRESULT holds the raw 32-bit code
// (e.g. 0x80042316); Name is its symbolic constant when known.
type Error struct {
	// Op is the API call that failed, e.g. "DoSnapshotSet".
	Op string
	// HRESULT is the failure code, truncated to 32 bits.
	HRESULT uint32
}

func (e *Error) Error() string {
	name := hresultName(e.HRESULT)
	if name == "" {
		return fmt.Sprintf("vss: %s failed: HRESULT 0x%08X", e.Op, e.HRESULT)
	}
	msg := fmt.Sprintf("vss: %s failed: %s (0x%08X)", e.Op, name, e.HRESULT)
	if hint := hresultHint(e.HRESULT); hint != "" {
		msg += ": " + hint
	}
	return msg
}

// Retryable reports whether the failure is transient per VSS guidance
// (another snapshot in progress, freeze/flush timeouts, retryable writer
// errors). Callers should bound retries and add jitter: snapshot creation
// is serialized machine-wide.
func (e *Error) Retryable() bool {
	switch e.HRESULT {
	case hrSnapshotSetInProgress,
		hrFlushWritesTimeout,
		hrHoldWritesTimeout,
		hrWriterErrorRetryable,
		hrWriterErrorTimeout:
		return true
	}
	return false
}

// IsRetryable reports whether err (or an error it wraps) is a transient VSS
// failure worth retrying.
func IsRetryable(err error) bool {
	var ve *Error
	return errors.As(err, &ve) && ve.Retryable()
}

// HRESULT values referenced in code paths. The full name table lives in
// hresultName.
const (
	hrOK                    = 0x00000000
	hrSFalse                = 0x00000001
	hrAccessDenied          = 0x80070005
	hrAsyncPending          = 0x00042309
	hrAsyncFinished         = 0x0004230A
	hrAsyncCancelled        = 0x0004230B
	hrBadState              = 0x80042301
	hrObjectNotFound        = 0x80042308
	hrVolumeNotSupported    = 0x8004230C
	hrSnapshotSetInProgress = 0x80042316
	hrFlushWritesTimeout    = 0x80042313
	hrHoldWritesTimeout     = 0x80042314
	hrWriterErrorTimeout    = 0x800423F2
	hrWriterErrorRetryable  = 0x800423F3
	hrRPCChangedMode        = 0x80010106
	hrRPCTooLate            = 0x80010119
	hrNoInterface           = 0x80004002
)

func hresultName(hr uint32) string {
	if n, ok := hresultNames[hr]; ok {
		return n
	}
	return ""
}

// hresultHint returns an actionable one-liner for the failures users hit
// most, so raw codes never reach a human undecorated.
func hresultHint(hr uint32) string {
	switch hr {
	case hrAccessDenied:
		return "the process must run elevated (Administrators)"
	case hrBadState:
		return "VSS methods called out of order or a components object reused"
	case hrSnapshotSetInProgress:
		return "another backup is creating a snapshot; retry with backoff"
	case hrVolumeNotSupported:
		return "this volume/filesystem cannot be snapshotted"
	case hrObjectNotFound:
		return "the snapshot no longer exists (it may have been deleted under load)"
	case hrRPCChangedMode:
		return "COM apartment conflict: another component initialized this thread as STA"
	}
	return ""
}

var hresultNames = map[uint32]string{
	0x80070005: "E_ACCESSDENIED",
	0x80004002: "E_NOINTERFACE",
	0x8000FFFF: "E_UNEXPECTED",
	0x80070057: "E_INVALIDARG",
	0x8007000E: "E_OUTOFMEMORY",
	0x800401F0: "CO_E_NOTINITIALIZED",
	0x80010106: "RPC_E_CHANGED_MODE",
	0x80010119: "RPC_E_TOO_LATE",

	0x00042309: "VSS_S_ASYNC_PENDING",
	0x0004230A: "VSS_S_ASYNC_FINISHED",
	0x0004230B: "VSS_S_ASYNC_CANCELLED",
	0x00042321: "VSS_S_SOME_SNAPSHOTS_NOT_IMPORTED",

	0x80042301: "VSS_E_BAD_STATE",
	0x80042302: "VSS_E_UNEXPECTED",
	0x80042304: "VSS_E_PROVIDER_NOT_REGISTERED",
	0x80042306: "VSS_E_PROVIDER_VETO",
	0x80042308: "VSS_E_OBJECT_NOT_FOUND",
	0x8004230C: "VSS_E_VOLUME_NOT_SUPPORTED",
	0x8004230E: "VSS_E_VOLUME_NOT_SUPPORTED_BY_PROVIDER",
	0x8004230F: "VSS_E_UNEXPECTED_PROVIDER_ERROR",
	0x80042312: "VSS_E_MAXIMUM_NUMBER_OF_VOLUMES_REACHED",
	0x80042313: "VSS_E_FLUSH_WRITES_TIMEOUT",
	0x80042314: "VSS_E_HOLD_WRITES_TIMEOUT",
	0x80042315: "VSS_E_UNEXPECTED_WRITER_ERROR",
	0x80042316: "VSS_E_SNAPSHOT_SET_IN_PROGRESS",
	0x80042317: "VSS_E_MAXIMUM_NUMBER_OF_SNAPSHOTS_REACHED",
	0x80042318: "VSS_E_WRITER_INFRASTRUCTURE",
	0x80042319: "VSS_E_WRITER_NOT_RESPONDING",
	0x8004231B: "VSS_E_UNSUPPORTED_CONTEXT",
	0x8004231D: "VSS_E_VOLUME_IN_USE",
	0x8004231E: "VSS_E_MAXIMUM_DIFFAREA_ASSOCIATIONS_REACHED",
	0x8004231F: "VSS_E_INSUFFICIENT_STORAGE",
	0x80042320: "VSS_E_NO_SNAPSHOTS_IMPORTED",
	0x80042325: "VSS_E_REVERT_IN_PROGRESS",
	0x80042327: "VSS_E_REBOOT_REQUIRED",
	0x80042328: "VSS_E_TRANSACTION_FREEZE_TIMEOUT",
	0x80042329: "VSS_E_TRANSACTION_THAW_TIMEOUT",
	0x8004232A: "VSS_E_UNSELECTED_VOLUME",
	0x8004232B: "VSS_E_SNAPSHOT_NOT_IN_SET",
	0x8004232D: "VSS_E_VOLUME_NOT_LOCAL",
	0x8004232E: "VSS_E_CLUSTER_TIMEOUT",
	0x800423F0: "VSS_E_WRITERERROR_INCONSISTENTSNAPSHOT",
	0x800423F1: "VSS_E_WRITERERROR_OUTOFRESOURCES",
	0x800423F2: "VSS_E_WRITERERROR_TIMEOUT",
	0x800423F3: "VSS_E_WRITERERROR_RETRYABLE",
	0x800423F4: "VSS_E_WRITERERROR_NONRETRYABLE",
	0x800423F5: "VSS_E_WRITERERROR_RECOVERY_FAILED",
	0x800423F8: "VSS_E_MISSING_DISK",
	0x800423F9: "VSS_E_MISSING_HIDDEN_VOLUME",
	0x800423FA: "VSS_E_MISSING_VOLUME",
	0x800423FB: "VSS_E_AUTORECOVERY_FAILED",
	0x800423FC: "VSS_E_DYNAMIC_DISK_ERROR",
	0x800423FF: "VSS_E_RESYNC_IN_PROGRESS",
	0x80042400: "VSS_E_CLUSTER_ERROR",
}

// hrFailed reports whether an HRESULT is a failure code.
func hrFailed(hr uint32) bool { return hr&0x80000000 != 0 }

// hrErr converts a raw HRESULT (as returned by SyscallN, so possibly
// sign-extended to 64 bits) into an *Error, or nil on success. Success
// codes with meaning (S_FALSE, VSS_S_*) are treated as success here;
// callers that care about them must inspect the raw value instead.
func hrErr(hr uintptr, op string) error {
	h := uint32(hr)
	if !hrFailed(h) {
		return nil
	}
	return &Error{Op: op, HRESULT: h}
}
