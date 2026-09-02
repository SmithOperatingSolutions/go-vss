//go:build windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// IID_IVssBackupComponents {665c1d5f-c218-414d-a05d-7fef5f9d5c86}
var iidIVssBackupComponents = guid{0x665c1d5f, 0xc218, 0x414d, [8]byte{0xa0, 0x5d, 0x7f, 0xef, 0x5f, 0x9d, 0x5c, 0x86}}

// backupComponents wraps IVssBackupComponents. All methods must run on the
// owning worker's COM thread. One instance is valid for exactly one backup
// or one Query operation — reuse causes VSS_E_BAD_STATE.
type backupComponents struct {
	vtbl *bcVtbl
}

// bcVtbl is the IVssBackupComponents vtable in vsbackup.h declaration
// order (NOT the alphabetical order Microsoft Learn displays). 51 slots:
// 3 IUnknown + 48 interface methods. The slot count is verified at init
// against the documented vsbackup.h ABI (see the panic guard below).
type bcVtbl struct {
	// IUnknown
	queryInterface uintptr // 0
	addRef         uintptr // 1
	release        uintptr // 2
	// IVssBackupComponents
	getWriterComponentsCount      uintptr // 3
	getWriterComponents           uintptr // 4
	initializeForBackup           uintptr // 5
	setBackupState                uintptr // 6
	initializeForRestore          uintptr // 7
	setRestoreState               uintptr // 8
	gatherWriterMetadata          uintptr // 9
	getWriterMetadataCount        uintptr // 10
	getWriterMetadata             uintptr // 11
	freeWriterMetadata            uintptr // 12
	addComponent                  uintptr // 13
	prepareForBackup              uintptr // 14
	abortBackup                   uintptr // 15
	gatherWriterStatus            uintptr // 16
	getWriterStatusCount          uintptr // 17
	freeWriterStatus              uintptr // 18
	getWriterStatus               uintptr // 19
	setBackupSucceeded            uintptr // 20
	setBackupOptions              uintptr // 21
	setSelectedForRestore         uintptr // 22
	setRestoreOptions             uintptr // 23
	setAdditionalRestores         uintptr // 24
	setPreviousBackupStamp        uintptr // 25
	saveAsXML                     uintptr // 26
	backupComplete                uintptr // 27
	addAlternativeLocationMapping uintptr // 28
	addRestoreSubcomponent        uintptr // 29
	setFileRestoreStatus          uintptr // 30
	addNewTarget                  uintptr // 31
	setRangesFilePath             uintptr // 32
	preRestore                    uintptr // 33
	postRestore                   uintptr // 34
	setContext                    uintptr // 35
	startSnapshotSet              uintptr // 36
	addToSnapshotSet              uintptr // 37
	doSnapshotSet                 uintptr // 38
	deleteSnapshots               uintptr // 39
	importSnapshots               uintptr // 40
	breakSnapshotSet              uintptr // 41
	getSnapshotProperties         uintptr // 42
	query                         uintptr // 43
	isVolumeSupported             uintptr // 44
	disableWriterClasses          uintptr // 45
	enableWriterClasses           uintptr // 46
	disableWriterInstances        uintptr // 47
	exposeSnapshot                uintptr // 48
	revertToSnapshot              uintptr // 49
	queryRevertStatus             uintptr // 50
}

func init() {
	const slots = 51
	if unsafe.Sizeof(bcVtbl{}) != slots*unsafe.Sizeof(uintptr(0)) {
		panic("vss: IVssBackupComponents vtable slot count is wrong")
	}
}

// createBackupComponents creates the requester object. Following the
// standard COM creation pattern, the raw result is treated as IUnknown and
// QueryInterface'd for IVssBackupComponents, which insulates against proxy
// layering differences across OS versions.
func createBackupComponents() (*backupComponents, error) {
	procs, err := loadProcs()
	if err != nil {
		return nil, err
	}
	var raw *backupComponents
	hr, _, _ := procs.createBackupComponents.Call(uintptr(unsafe.Pointer(&raw)))
	if err := hrErr(hr, "CreateVssBackupComponents"); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, &Error{Op: "CreateVssBackupComponents", HRESULT: 0x8000FFFF}
	}
	var bc *backupComponents
	hr, _, _ = syscall.SyscallN(raw.vtbl.queryInterface,
		uintptr(unsafe.Pointer(raw)),
		uintptr(unsafe.Pointer(&iidIVssBackupComponents)),
		uintptr(unsafe.Pointer(&bc)),
	)
	raw.Release()
	if err := hrErr(hr, "QueryInterface(IVssBackupComponents)"); err != nil {
		return nil, err
	}
	return bc, nil
}

func (v *backupComponents) Release() {
	syscall.SyscallN(v.vtbl.release, uintptr(unsafe.Pointer(v)))
}

func (v *backupComponents) InitializeForBackup() error {
	hr, _, _ := syscall.SyscallN(v.vtbl.initializeForBackup,
		uintptr(unsafe.Pointer(v)), 0)
	return hrErr(hr, "InitializeForBackup")
}

func (v *backupComponents) SetContext(ctx uint32) error {
	hr, _, _ := syscall.SyscallN(v.vtbl.setContext,
		uintptr(unsafe.Pointer(v)), uintptr(ctx))
	return hrErr(hr, "SetContext")
}

// SetBackupState declares backup semantics to writers. The bool parameters
// are C++ bool (1 byte, passed widened in a register slot).
func (v *backupComponents) SetBackupState(selectComponents, bootableSystemState bool, backupType int32, partialFileSupport bool) error {
	b := func(x bool) uintptr {
		if x {
			return 1
		}
		return 0
	}
	hr, _, _ := syscall.SyscallN(v.vtbl.setBackupState,
		uintptr(unsafe.Pointer(v)),
		b(selectComponents), b(bootableSystemState),
		uintptr(uint32(backupType)), b(partialFileSupport))
	return hrErr(hr, "SetBackupState")
}

func (v *backupComponents) asyncCall(slot uintptr, op string) (*vssAsync, error) {
	var a *vssAsync
	hr, _, _ := syscall.SyscallN(slot,
		uintptr(unsafe.Pointer(v)), uintptr(unsafe.Pointer(&a)))
	if err := hrErr(hr, op); err != nil {
		return nil, err
	}
	if a == nil {
		return nil, &Error{Op: op, HRESULT: 0x8000FFFF}
	}
	return a, nil
}

func (v *backupComponents) GatherWriterMetadata() (*vssAsync, error) {
	return v.asyncCall(v.vtbl.gatherWriterMetadata, "GatherWriterMetadata")
}

func (v *backupComponents) FreeWriterMetadata() {
	syscall.SyscallN(v.vtbl.freeWriterMetadata, uintptr(unsafe.Pointer(v)))
}

func (v *backupComponents) PrepareForBackup() (*vssAsync, error) {
	return v.asyncCall(v.vtbl.prepareForBackup, "PrepareForBackup")
}

func (v *backupComponents) DoSnapshotSet() (*vssAsync, error) {
	return v.asyncCall(v.vtbl.doSnapshotSet, "DoSnapshotSet")
}

func (v *backupComponents) BackupComplete() (*vssAsync, error) {
	return v.asyncCall(v.vtbl.backupComplete, "BackupComplete")
}

func (v *backupComponents) GatherWriterStatus() (*vssAsync, error) {
	return v.asyncCall(v.vtbl.gatherWriterStatus, "GatherWriterStatus")
}

// AbortBackup unwinds writers on the error path. VSS_E_BAD_STATE from an
// abort with nothing to abort is harmless and ignored.
func (v *backupComponents) AbortBackup() {
	syscall.SyscallN(v.vtbl.abortBackup, uintptr(unsafe.Pointer(v)))
}

func (v *backupComponents) StartSnapshotSet() (guid, error) {
	var setID guid
	hr, _, _ := syscall.SyscallN(v.vtbl.startSnapshotSet,
		uintptr(unsafe.Pointer(v)), uintptr(unsafe.Pointer(&setID)))
	return setID, hrErr(hr, "StartSnapshotSet")
}

func (v *backupComponents) GetWriterStatusCount() (uint32, error) {
	var n uint32
	hr, _, _ := syscall.SyscallN(v.vtbl.getWriterStatusCount,
		uintptr(unsafe.Pointer(v)), uintptr(unsafe.Pointer(&n)))
	return n, hrErr(hr, "GetWriterStatusCount")
}

func (v *backupComponents) FreeWriterStatus() {
	syscall.SyscallN(v.vtbl.freeWriterStatus, uintptr(unsafe.Pointer(v)))
}

// GetWriterStatus returns one writer's outcome. The name BSTR is freed
// here; failure HRESULT 0 means no failure.
func (v *backupComponents) GetWriterStatus(i uint32) (WriterStatus, error) {
	var (
		instance, writer guid
		name             *uint16
		state            int32
		hrFailure        int32
	)
	hr, _, _ := syscall.SyscallN(v.vtbl.getWriterStatus,
		uintptr(unsafe.Pointer(v)),
		uintptr(i),
		uintptr(unsafe.Pointer(&instance)),
		uintptr(unsafe.Pointer(&writer)),
		uintptr(unsafe.Pointer(&name)),
		uintptr(unsafe.Pointer(&state)),
		uintptr(unsafe.Pointer(&hrFailure)),
	)
	if err := hrErr(hr, "GetWriterStatus"); err != nil {
		return WriterStatus{}, err
	}
	ws := WriterStatus{
		InstanceID: instance.String(),
		WriterID:   writer.String(),
		Name:       utf16PtrToString(name),
		State:      WriterState(state),
	}
	sysFreeString(name)
	if f := uint32(hrFailure); hrFailed(f) {
		ws.Failure = &Error{Op: "writer " + ws.Name, HRESULT: f}
	}
	return ws, nil
}

// The five methods that take a VSS_ID (GUID) by value have
// architecture-specific call shapes; see guidargs_*.go.

func (v *backupComponents) AddToSnapshotSet(volume string, provider guid) (guid, error) {
	volp, err := utf16Ptr(volume)
	if err != nil {
		return guid{}, err
	}
	var snapID guid
	hr := v.addToSnapshotSetCall(volp, provider, &snapID)
	return snapID, hrErr(hr, "AddToSnapshotSet")
}

func (v *backupComponents) IsVolumeSupported(provider guid, volume string) (bool, error) {
	volp, err := utf16Ptr(volume)
	if err != nil {
		return false, err
	}
	var supported int32
	hr := v.isVolumeSupportedCall(provider, volp, &supported)
	if err := hrErr(hr, "IsVolumeSupported"); err != nil {
		return false, err
	}
	return supported != 0, nil
}

func (v *backupComponents) GetSnapshotProperties(id guid) (*vssSnapshotProp, error) {
	var prop vssSnapshotProp
	hr := v.getSnapshotPropertiesCall(id, &prop)
	if err := hrErr(hr, "GetSnapshotProperties"); err != nil {
		return nil, err
	}
	return &prop, nil
}

// DeleteSnapshots deletes by object ID (snapshot or snapshot-set). Returns
// how many were deleted and, on partial failure, the first survivor.
func (v *backupComponents) DeleteSnapshots(id guid, objType int32, force bool) (int32, guid, error) {
	var (
		deleted    int32
		nonDeleted guid
		f          uintptr
	)
	if force {
		f = 1
	}
	hr := v.deleteSnapshotsCall(id, objType, f, &deleted, &nonDeleted)
	return deleted, nonDeleted, hrErr(hr, "DeleteSnapshots")
}

func (v *backupComponents) Query(returnedType int32) (*enumObject, error) {
	var e *enumObject
	hr := v.queryCall(guidNull, vssObjectNone, returnedType, &e)
	if err := hrErr(hr, "Query"); err != nil {
		return nil, err
	}
	if e == nil {
		return nil, &Error{Op: "Query", HRESULT: 0x8000FFFF}
	}
	return e, nil
}

// utf16Ptr converts a Go string, erroring on embedded NUL instead of
// panicking like StringToUTF16Ptr would.
func utf16Ptr(s string) (*uint16, error) {
	return windows.UTF16PtrFromString(s)
}
