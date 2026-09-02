//go:build windows

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// examineWriterMetadata wraps IVssExamineWriterMetadata (vsbackup.h) — a
// local (non-remotable) C++ interface valid only until FreeWriterMetadata.
type examineWriterMetadata struct {
	vtbl *ewmVtbl
}

// ewmVtbl follows vsbackup.h declaration order (reference doc §9.3).
type ewmVtbl struct {
	queryInterface              uintptr // 0
	addRef                      uintptr // 1
	release                     uintptr // 2
	getIdentity                 uintptr // 3
	getFileCounts               uintptr // 4
	getIncludeFile              uintptr // 5
	getExcludeFile              uintptr // 6
	getComponent                uintptr // 7
	getRestoreMethod            uintptr // 8
	getAlternateLocationMapping uintptr // 9
	getBackupSchema             uintptr // 10
	getDocument                 uintptr // 11
	saveAsXML                   uintptr // 12
	loadFromXML                 uintptr // 13
}

func (m *examineWriterMetadata) Release() {
	syscall.SyscallN(m.vtbl.release, uintptr(unsafe.Pointer(m)))
}

// WriterName returns the writer's display name from GetIdentity.
func (m *examineWriterMetadata) WriterName() (string, error) {
	var (
		instance, writer guid
		name             *uint16
		usage, source    int32
	)
	hr, _, _ := syscall.SyscallN(m.vtbl.getIdentity,
		uintptr(unsafe.Pointer(m)),
		uintptr(unsafe.Pointer(&instance)),
		uintptr(unsafe.Pointer(&writer)),
		uintptr(unsafe.Pointer(&name)),
		uintptr(unsafe.Pointer(&usage)),
		uintptr(unsafe.Pointer(&source)),
	)
	if err := hrErr(hr, "GetIdentity"); err != nil {
		return "", err
	}
	s := utf16PtrToString(name)
	sysFreeString(name)
	return s, nil
}

// FileCounts returns (includeFiles, excludeFiles, components).
func (m *examineWriterMetadata) FileCounts() (uint32, uint32, uint32, error) {
	var incl, excl, comp uint32
	hr, _, _ := syscall.SyscallN(m.vtbl.getFileCounts,
		uintptr(unsafe.Pointer(m)),
		uintptr(unsafe.Pointer(&incl)),
		uintptr(unsafe.Pointer(&excl)),
		uintptr(unsafe.Pointer(&comp)),
	)
	if err := hrErr(hr, "GetFileCounts"); err != nil {
		return 0, 0, 0, err
	}
	return incl, excl, comp, nil
}

func (m *examineWriterMetadata) ExcludeFile(i uint32) (*wmFiledesc, error) {
	var fd *wmFiledesc
	hr, _, _ := syscall.SyscallN(m.vtbl.getExcludeFile,
		uintptr(unsafe.Pointer(m)),
		uintptr(i),
		uintptr(unsafe.Pointer(&fd)),
	)
	if err := hrErr(hr, "GetExcludeFile"); err != nil {
		return nil, err
	}
	if fd == nil {
		return nil, &Error{Op: "GetExcludeFile", HRESULT: 0x8000FFFF}
	}
	return fd, nil
}

// wmFiledesc wraps IVssWMFiledesc (reference doc §9.4).
type wmFiledesc struct {
	vtbl *wmfVtbl
}

type wmfVtbl struct {
	queryInterface       uintptr // 0
	addRef               uintptr // 1
	release              uintptr // 2
	getPath              uintptr // 3
	getFilespec          uintptr // 4
	getRecursive         uintptr // 5
	getAlternateLocation uintptr // 6
	getBackupTypeMask    uintptr // 7
}

func (f *wmFiledesc) Release() {
	syscall.SyscallN(f.vtbl.release, uintptr(unsafe.Pointer(f)))
}

func (f *wmFiledesc) bstrCall(slot uintptr, op string) (string, error) {
	var b *uint16
	hr, _, _ := syscall.SyscallN(slot,
		uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(&b)))
	if err := hrErr(hr, op); err != nil {
		return "", err
	}
	s := utf16PtrToString(b)
	sysFreeString(b)
	return s, nil
}

func (f *wmFiledesc) Path() (string, error) {
	return f.bstrCall(f.vtbl.getPath, "IVssWMFiledesc::GetPath")
}

func (f *wmFiledesc) Filespec() (string, error) {
	return f.bstrCall(f.vtbl.getFilespec, "IVssWMFiledesc::GetFilespec")
}

func (f *wmFiledesc) Recursive() (bool, error) {
	var b uint8 // C++ bool: one byte
	hr, _, _ := syscall.SyscallN(f.vtbl.getRecursive,
		uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(&b)))
	if err := hrErr(hr, "IVssWMFiledesc::GetRecursive"); err != nil {
		return false, err
	}
	return b != 0, nil
}

// GetWriterMetadataCount and GetWriterMetadata are valid only between
// GatherWriterMetadata completing and FreeWriterMetadata.

func (v *backupComponents) GetWriterMetadataCount() (uint32, error) {
	var n uint32
	hr, _, _ := syscall.SyscallN(v.vtbl.getWriterMetadataCount,
		uintptr(unsafe.Pointer(v)), uintptr(unsafe.Pointer(&n)))
	return n, hrErr(hr, "GetWriterMetadataCount")
}

func (v *backupComponents) GetWriterMetadata(i uint32) (*examineWriterMetadata, error) {
	var (
		instance guid
		md       *examineWriterMetadata
	)
	hr, _, _ := syscall.SyscallN(v.vtbl.getWriterMetadata,
		uintptr(unsafe.Pointer(v)),
		uintptr(i),
		uintptr(unsafe.Pointer(&instance)),
		uintptr(unsafe.Pointer(&md)),
	)
	if err := hrErr(hr, "GetWriterMetadata"); err != nil {
		return nil, err
	}
	if md == nil {
		return nil, &Error{Op: "GetWriterMetadata", HRESULT: 0x8000FFFF}
	}
	return md, nil
}

// collectExcludeRules walks every writer's metadata and gathers its
// exclude-file declarations. Best-effort per writer: a writer whose
// metadata cannot be read is skipped and reported in the joined error, so
// one broken writer doesn't fail the snapshot.
func collectExcludeRules(bc *backupComponents) ([]ExcludeRule, error) {
	n, err := bc.GetWriterMetadataCount()
	if err != nil {
		return nil, err
	}
	var (
		rules []ExcludeRule
		errs  []error
	)
	for i := uint32(0); i < n; i++ {
		md, err := bc.GetWriterMetadata(i)
		if err != nil {
			errs = append(errs, fmt.Errorf("writer %d: %w", i, err))
			continue
		}
		if err := func() error {
			defer md.Release()
			name, err := md.WriterName()
			if err != nil {
				return err
			}
			_, nExcl, _, err := md.FileCounts()
			if err != nil {
				return fmt.Errorf("writer %q: %w", name, err)
			}
			for j := uint32(0); j < nExcl; j++ {
				fd, err := md.ExcludeFile(j)
				if err != nil {
					return fmt.Errorf("writer %q exclude %d: %w", name, j, err)
				}
				rule, err := ruleFromFiledesc(fd, name)
				fd.Release()
				if err != nil {
					return fmt.Errorf("writer %q exclude %d: %w", name, j, err)
				}
				if rule.Path == "" {
					continue // nothing to scope against; skip rather than over-match
				}
				rules = append(rules, rule)
			}
			return nil
		}(); err != nil {
			errs = append(errs, err)
		}
	}
	return rules, errors.Join(errs...)
}

func ruleFromFiledesc(fd *wmFiledesc, writer string) (ExcludeRule, error) {
	raw, err := fd.Path()
	if err != nil {
		return ExcludeRule{}, err
	}
	spec, err := fd.Filespec()
	if err != nil {
		return ExcludeRule{}, err
	}
	rec, err := fd.Recursive()
	if err != nil {
		return ExcludeRule{}, err
	}
	return ExcludeRule{
		Writer:    writer,
		Path:      expandEnv(raw),
		RawPath:   raw,
		FileSpec:  spec,
		Recursive: rec,
	}, nil
}

// expandEnv expands %VAR% references the way writers declare them.
// Returns the input unchanged on any failure.
func expandEnv(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	src, err := utf16Ptr(s)
	if err != nil {
		return s
	}
	n, err := windows.ExpandEnvironmentStrings(src, nil, 0)
	if err != nil || n == 0 {
		return s
	}
	buf := make([]uint16, n)
	if _, err := windows.ExpandEnvironmentStrings(src, &buf[0], n); err != nil {
		return s
	}
	return windows.UTF16ToString(buf)
}
