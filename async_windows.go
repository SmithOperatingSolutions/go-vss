//go:build windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"context"
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

// vssAsync wraps IVssAsync (vss.h). Every asynchronous IVssBackupComponents
// method returns a fresh instance.
type vssAsync struct {
	vtbl *asyncVtbl
}

type asyncVtbl struct {
	queryInterface uintptr // 0
	addRef         uintptr // 1
	release        uintptr // 2
	cancel         uintptr // 3
	wait           uintptr // 4
	queryStatus    uintptr // 5
}

func (a *vssAsync) Release() {
	syscall.SyscallN(a.vtbl.release, uintptr(unsafe.Pointer(a)))
}

func (a *vssAsync) Cancel() {
	syscall.SyscallN(a.vtbl.cancel, uintptr(unsafe.Pointer(a)))
}

func (a *vssAsync) Wait(ms uint32) error {
	hr, _, _ := syscall.SyscallN(a.vtbl.wait,
		uintptr(unsafe.Pointer(a)), uintptr(ms))
	return hrErr(hr, "IVssAsync::Wait")
}

func (a *vssAsync) QueryStatus() (uint32, error) {
	var hrStatus int32
	hr, _, _ := syscall.SyscallN(a.vtbl.queryStatus,
		uintptr(unsafe.Pointer(a)),
		uintptr(unsafe.Pointer(&hrStatus)), 0)
	if err := hrErr(hr, "IVssAsync::QueryStatus"); err != nil {
		return 0, err
	}
	return uint32(hrStatus), nil
}

// awaitAsync waits for a VSS async operation with a hard deadline and
// cooperative context cancellation. Wait alone is not a sufficient
// completion signal on all Windows versions — QueryStatus is authoritative.
// Never waits unbounded: a wedged writer must produce a diagnosable timeout,
// not a hang. Releases the async object in all paths.
func awaitAsync(ctx context.Context, a *vssAsync, op string, timeout time.Duration) error {
	defer a.Release()
	deadline := time.Now().Add(timeout)

	for {
		select {
		case <-ctx.Done():
			a.Cancel()
			return fmt.Errorf("vss: %s: %w", op, ctx.Err())
		default:
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			a.Cancel() // fail closed; do not leave it running
			return fmt.Errorf("vss: %s timed out after %s", op, timeout)
		}
		slice := remaining
		if slice > time.Second {
			slice = time.Second // stay responsive to ctx cancellation
		}
		if err := a.Wait(uint32(slice.Milliseconds())); err != nil {
			a.Cancel()
			return fmt.Errorf("vss: %s: %w", op, err)
		}

		status, err := a.QueryStatus()
		if err != nil {
			a.Cancel()
			return fmt.Errorf("vss: %s: %w", op, err)
		}
		switch status {
		case hrAsyncFinished:
			return nil
		case hrAsyncPending:
			continue
		case hrAsyncCancelled:
			return fmt.Errorf("vss: %s was cancelled", op)
		default:
			if hrFailed(status) {
				return &Error{Op: op, HRESULT: status}
			}
			// Unknown success code: treat as finished.
			return nil
		}
	}
}
