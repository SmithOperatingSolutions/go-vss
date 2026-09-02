//go:build windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modOle32                 = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx       = modOle32.NewProc("CoInitializeEx")
	procCoUninitialize       = modOle32.NewProc("CoUninitialize")
	procCoInitializeSecurity = modOle32.NewProc("CoInitializeSecurity")

	modOleAut32       = windows.NewLazySystemDLL("oleaut32.dll")
	procSysFreeString = modOleAut32.NewProc("SysFreeString")
)

const coinitMultithreaded = 0x0

// initComSecurity runs once per process. Writers call back into the
// requester over COM; these are the settings the Microsoft vshadow sample
// uses (PKT_PRIVACY / IMPersonation level IDENTIFY). RPC_E_TOO_LATE means
// something else in the process already configured COM security — accept it.
var initComSecurity = sync.OnceValue(func() error {
	const (
		rpcAuthnLevelPktPrivacy = 6
		rpcImpLevelIdentify     = 2
	)
	hr, _, _ := procCoInitializeSecurity.Call(
		0, ^uintptr(0) /* -1: let COM choose auth services */, 0, 0,
		rpcAuthnLevelPktPrivacy,
		rpcImpLevelIdentify,
		0, 0 /* EOAC_NONE */, 0,
	)
	if h := uint32(hr); hrFailed(h) && h != hrRPCTooLate {
		return &Error{Op: "CoInitializeSecurity", HRESULT: h}
	}
	return nil
})

// worker owns one OS thread with a live COM MTA apartment. COM apartment
// state is per-OS-thread and goroutines migrate, so every VSS call —
// including Release — must be funneled through the same locked thread.
type worker struct {
	cmds chan func()
	quit chan struct{}
}

func newWorker() (*worker, error) {
	w := &worker{cmds: make(chan func()), quit: make(chan struct{})}
	ready := make(chan error, 1)

	go func() {
		// Intentionally no UnlockOSThread: when this goroutine exits, the
		// runtime destroys the thread instead of returning it to the pool,
		// which is exactly right for a thread that called CoInitializeEx.
		runtime.LockOSThread()

		hr, _, _ := procCoInitializeEx.Call(0, coinitMultithreaded)
		switch uint32(hr) {
		case hrOK, hrSFalse:
		default:
			ready <- &Error{Op: "CoInitializeEx", HRESULT: uint32(hr)}
			return
		}
		if err := initComSecurity(); err != nil {
			procCoUninitialize.Call()
			ready <- err
			return
		}
		ready <- nil

		for {
			select {
			case fn := <-w.cmds:
				fn()
			case <-w.quit:
				procCoUninitialize.Call()
				return
			}
		}
	}()

	if err := <-ready; err != nil {
		return nil, fmt.Errorf("vss: COM initialization: %w", err)
	}
	return w, nil
}

// do runs fn on the COM thread and returns its error.
func (w *worker) do(fn func() error) error {
	var err error
	done := make(chan struct{})
	w.cmds <- func() {
		defer close(done)
		err = fn()
	}
	<-done
	return err
}

func (w *worker) stop() { close(w.quit) }

func sysFreeString(b *uint16) {
	if b != nil {
		procSysFreeString.Call(uintptr(unsafe.Pointer(b)))
	}
}
