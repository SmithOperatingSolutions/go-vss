// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

// Command vssdemo is a manual smoke test for the vss package: it creates a
// snapshot, lists shadow copies, reads a file out of the snapshot, and
// cleans up. Run it elevated on Windows.
//
//	vssdemo list
//	vssdemo snap C:\
//	vssdemo read C:\ Windows\System32\drivers\etc\hosts
//	vssdemo walk C:\ Users\bob     (manifest listing with excludes applied)
//	vssdemo hold C:\ 5m            (keep a snapshot alive for inspection)
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"time"

	vss "github.com/SmithOperatingSolutions/go-vss"
)

func main() {
	flag.Parse()
	if err := run(flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: vssdemo list | snap <vol> | read <vol> <relpath> | walk <vol> [subdir] | usn <vol> [journalID nextUSN] | hold <vol> <duration>")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	switch args[0] {
	case "list":
		snaps, err := vss.List(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("%d shadow copies:\n", len(snaps))
		for _, s := range snaps {
			fmt.Printf("  %s  %s  state=%s\n    volume=%s\n    device=%s\n",
				s.ID, s.CreatedAt.Format(time.RFC3339), s.State, s.Volume, s.DeviceObject)
		}
		return nil

	case "snap":
		if len(args) < 2 {
			return fmt.Errorf("usage: vssdemo snap <vol>")
		}
		return withSnapshot(ctx, args[1], func(set *vss.SnapshotSet, s *vss.Snapshot) error {
			entries, err := fs.ReadDir(s.FS(), ".")
			if err != nil {
				return err
			}
			fmt.Printf("snapshot %s\ndevice   %s\nroot has %d entries\n", s.ID, s.DeviceObject, len(entries))
			return nil
		})

	case "read":
		if len(args) < 3 {
			return fmt.Errorf("usage: vssdemo read <vol> <relpath>")
		}
		return withSnapshot(ctx, args[1], func(set *vss.SnapshotSet, s *vss.Snapshot) error {
			f, err := s.Open(args[2])
			if err != nil {
				return err
			}
			defer f.Close()
			n, err := io.Copy(os.Stdout, f)
			fmt.Fprintf(os.Stderr, "\n-- %d bytes from snapshot %s\n", n, s.ID)
			return err
		})

	case "walk":
		if len(args) < 2 {
			return fmt.Errorf("usage: vssdemo walk <vol> [subdir]")
		}
		sub := ""
		if len(args) > 2 {
			sub = args[2]
		}
		return withSnapshot(ctx, args[1], func(set *vss.SnapshotSet, s *vss.Snapshot) error {
			rules, err := set.ExcludeRules()
			if err != nil {
				fmt.Fprintln(os.Stderr, "warning: incomplete writer metadata:", err)
			}
			rules = append(rules, vss.SystemExcludes(s.VolumePath)...)
			var files, dirs, excluded int
			var bytes int64
			err = s.Walk(ctx, sub, vss.WalkOptions{Excludes: rules, IncludeExcluded: true}, func(e *vss.Entry, err error) error {
				if err != nil {
					fmt.Fprintf(os.Stderr, "! %s: %v\n", e.Path, err)
					return nil
				}
				switch {
				case e.Excluded:
					excluded++
					fmt.Printf("x %s (excluded by %s)\n", e.Path, e.ExcludedBy.Writer)
					return nil
				case e.IsReparsePoint():
					fmt.Printf("@ %s (reparse 0x%08X, not followed)\n", e.Path, e.ReparseTag)
					return nil
				case e.IsDir:
					dirs++
					fmt.Printf("d %s\n", e.Path)
				default:
					files++
					bytes += e.Size
					fmt.Printf("f %s (%d bytes, mtime %s)\n", e.Path, e.Size, e.Modified.Format(time.RFC3339))
				}
				return nil
			})
			fmt.Fprintf(os.Stderr, "-- %d files (%d bytes), %d dirs, %d excluded\n", files, bytes, dirs, excluded)
			return err
		})

	case "usn":
		if len(args) < 2 {
			return fmt.Errorf("usage: vssdemo usn <vol> [journalID nextUSN]")
		}
		return withSnapshot(ctx, args[1], func(set *vss.SnapshotSet, s *vss.Snapshot) error {
			info, err := s.USNJournal()
			if err != nil {
				return err
			}
			cur := info.Cursor()
			fmt.Printf("journal id=%#x first=%d next=%d max-size=%d\ncursor to persist: %d %d\n",
				info.ID, info.FirstUSN, info.NextUSN, info.MaximumSize, cur.JournalID, cur.NextUSN)
			if len(args) < 4 {
				return nil
			}
			var prev vss.USNCursor
			if _, err := fmt.Sscanf(args[2]+" "+args[3], "%v %v", &prev.JournalID, &prev.NextUSN); err != nil {
				return fmt.Errorf("parsing cursor: %w", err)
			}
			changes, next, err := s.USNChangesSince(ctx, prev)
			if err != nil {
				return err
			}
			for _, c := range changes {
				mark := "~"
				if c.Deleted {
					mark = "-"
				} else if c.Reasons&vss.USNReasonFileCreate != 0 {
					mark = "+"
				}
				p := c.Path
				if !c.PathKnown {
					p = "<unresolved> " + c.Name
				}
				fmt.Printf("%s %s (reasons %#x)\n", mark, p, c.Reasons)
			}
			fmt.Fprintf(os.Stderr, "-- %d changes; next cursor: %d %d\n", len(changes), next.JournalID, next.NextUSN)
			return nil
		})

	case "hold":
		if len(args) < 3 {
			return fmt.Errorf("usage: vssdemo hold <vol> <duration>")
		}
		d, err := time.ParseDuration(args[2])
		if err != nil {
			return err
		}
		return withSnapshot(ctx, args[1], func(set *vss.SnapshotSet, s *vss.Snapshot) error {
			fmt.Printf("holding snapshot %s at %s for %s (Ctrl-C to release early)\n", s.ID, s.DeviceObject, d)
			select {
			case <-time.After(d):
			case <-ctx.Done():
			}
			return nil
		})
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func withSnapshot(ctx context.Context, vol string, fn func(*vss.SnapshotSet, *vss.Snapshot) error) error {
	start := time.Now()
	set, err := vss.Create(ctx, vol)
	if err != nil {
		return err
	}
	defer func() {
		if err := set.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "cleanup:", err)
		}
	}()
	fmt.Fprintf(os.Stderr, "snapshot created in %s\n", time.Since(start).Round(time.Millisecond))
	for _, w := range set.Degraded() {
		fmt.Fprintf(os.Stderr, "warning: writer %q degraded: state=%s err=%v\n", w.Name, w.State, w.Failure)
	}
	return fn(set, set.Snapshots()[0])
}
