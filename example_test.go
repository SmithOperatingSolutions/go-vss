// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"time"

	vss "github.com/SmithOperatingSolutions/go-vss"
)

// The canonical backup flow: snapshot, read, clean up.
func Example() {
	ctx := context.Background()

	set, err := vss.Create(ctx, `C:\`)
	if err != nil {
		log.Fatal(err) // wraps vss.ErrUnsupported off-Windows; *vss.Error with HRESULT on VSS failures
	}
	defer set.Close() // notifies writers, deletes the snapshot

	snap := set.Snapshots()[0]

	// Standard fs.FS over the frozen volume (forward-slash paths).
	data, err := fs.ReadFile(snap.FS(), "Users/bob/Documents/ledger.db")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(len(data))
}

// Building a backup manifest: walk with writer + system + custom excludes.
func ExampleSnapshot_Walk() {
	ctx := context.Background()
	set, _ := vss.Create(ctx, `C:\`)
	defer set.Close()
	snap := set.Snapshots()[0]

	rules, _ := set.ExcludeRules()                                // what writers declare
	rules = append(rules, vss.SystemExcludes(snap.VolumePath)...) // kernel-scrubbed files
	rules = append(rules, vss.ExcludeRule{                        // your own policy
		Path: `C:\`, FileSpec: "*.tmp", Recursive: true,
	})

	err := snap.Walk(ctx, "", vss.WalkOptions{Excludes: rules}, func(e *vss.Entry, err error) error {
		if err != nil {
			log.Printf("unreadable dir %s: %v", e.Path, err)
			return nil // keep walking
		}
		if e.IsReparsePoint() {
			return nil // reported, never followed
		}
		if !e.IsDir {
			fmt.Println(e.Path, e.Size, e.Modified)
		}
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
}

// Incremental backups: persist the cursor, diff on the next run.
func ExampleSnapshot_USNChangesSince() {
	ctx := context.Background()

	// Backup N stored this after succeeding:
	previous := vss.USNCursor{JournalID: 0x1CC0FFEE, NextUSN: 123456}

	set, _ := vss.Create(ctx, `C:\`)
	defer set.Close()
	snap := set.Snapshots()[0]

	changes, next, err := snap.USNChangesSince(ctx, previous)
	switch {
	case errors.Is(err, vss.ErrJournalReset), errors.Is(err, vss.ErrTooManyChanges):
		// Fail closed: the journal can't prove what changed — full walk.
		// (Run snap.Walk over everything instead.)
	case err != nil:
		log.Fatal(err)
	default:
		for _, c := range changes {
			if c.Deleted {
				fmt.Println("gone:", c.Path)
				continue
			}
			fmt.Println("changed:", c.Path) // re-read via snap.Open(c.Path)
		}
		_ = next // persist after the backup succeeds (== snap.USNCursor)
	}
}

// Multi-volume atomic sets, timeouts, and degraded-writer handling.
func ExampleCreateSet() {
	ctx := context.Background()

	set, err := vss.CreateSet(ctx, []string{`C:\`, `D:\`},
		vss.WithTimeouts(vss.Timeouts{Snapshot: 5 * time.Minute}))
	if err != nil {
		if vss.IsRetryable(err) {
			// Another backup product holds the machine-wide snapshot
			// lock — retry with backoff.
		}
		log.Fatal(err)
	}
	defer set.Close()

	for _, w := range set.Degraded() {
		// Snapshot is readable, but this writer's application data may
		// be inconsistent inside it — surface it, don't hide it.
		log.Printf("degraded writer %q: %v (%v)", w.Name, w.State, w.Failure)
	}

	for _, snap := range set.Snapshots() {
		fmt.Println(snap.VolumePath, "->", snap.DeviceObject)
	}

	// During long reads, confirm the snapshots still exist (Windows
	// deletes shadow copies silently when diff-area space runs out).
	if err := set.Verify(ctx); err != nil {
		log.Fatal(err) // the backup is invalid, not partial
	}
}
