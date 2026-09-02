// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Smith Operating Solutions

package vss

import (
	"errors"
	"testing"
)

func TestAppendRuns(t *testing.T) {
	const cs = 4096

	t.Run("single run", func(t *testing.T) {
		out, next, err := appendRuns(nil, 0, []vcnRun{{NextVCN: 4, LCN: 100}}, cs)
		if err != nil {
			t.Fatal(err)
		}
		if next != 4 || len(out) != 1 {
			t.Fatalf("next=%d out=%+v", next, out)
		}
		want := Extent{FileOffset: 0, VolumeOffset: 100 * cs, Length: 4 * cs}
		if out[0] != want {
			t.Errorf("got %+v, want %+v", out[0], want)
		}
	})

	t.Run("sparse hole omitted, offsets preserved", func(t *testing.T) {
		// clusters [0,4) at LCN 50, [4,10) sparse, [10,16) at LCN 80
		out, next, err := appendRuns(nil, 0, []vcnRun{
			{NextVCN: 4, LCN: 50},
			{NextVCN: 10, LCN: -1},
			{NextVCN: 16, LCN: 80},
		}, cs)
		if err != nil {
			t.Fatal(err)
		}
		if next != 16 || len(out) != 2 {
			t.Fatalf("next=%d out=%+v", next, out)
		}
		if out[0] != (Extent{0, 50 * cs, 4 * cs}) {
			t.Errorf("first extent %+v", out[0])
		}
		// The post-hole extent must carry the hole in its FileOffset:
		// misplacing it would shift every byte after a sparse region.
		if out[1] != (Extent{10 * cs, 80 * cs, 6 * cs}) {
			t.Errorf("post-hole extent %+v", out[1])
		}
	})

	t.Run("continuation across MORE_DATA rounds", func(t *testing.T) {
		out, next, err := appendRuns(nil, 0, []vcnRun{{NextVCN: 8, LCN: 10}}, cs)
		if err != nil {
			t.Fatal(err)
		}
		out, next, err = appendRuns(out, next, []vcnRun{{NextVCN: 12, LCN: 200}}, cs)
		if err != nil {
			t.Fatal(err)
		}
		if next != 12 || len(out) != 2 {
			t.Fatalf("next=%d out=%+v", next, out)
		}
		if out[1] != (Extent{8 * cs, 200 * cs, 4 * cs}) {
			t.Errorf("continued extent %+v", out[1])
		}
	})

	t.Run("non-monotonic VCN fails closed", func(t *testing.T) {
		_, _, err := appendRuns(nil, 5, []vcnRun{{NextVCN: 5, LCN: 10}}, cs)
		if !errors.Is(err, errNonMonotonicVCN) {
			t.Errorf("equal VCN: %v", err)
		}
		_, _, err = appendRuns(nil, 5, []vcnRun{{NextVCN: 3, LCN: 10}}, cs)
		if !errors.Is(err, errNonMonotonicVCN) {
			t.Errorf("backwards VCN: %v", err)
		}
		// A valid prefix before the bad run must not be silently kept as
		// the final answer by callers: the error is the contract.
	})

	t.Run("invalid cluster size", func(t *testing.T) {
		for _, cs := range []int64{0, -4096} {
			if _, _, err := appendRuns(nil, 0, []vcnRun{{NextVCN: 1, LCN: 1}}, cs); !errors.Is(err, errBadClusterSize) {
				t.Errorf("cluster size %d: %v", cs, err)
			}
		}
	})

	t.Run("empty runs are a no-op", func(t *testing.T) {
		out, next, err := appendRuns([]Extent{{1, 2, 3}}, 7, nil, cs)
		if err != nil || next != 7 || len(out) != 1 {
			t.Errorf("out=%+v next=%d err=%v", out, next, err)
		}
	})

	t.Run("cluster size variations", func(t *testing.T) {
		for _, cs := range []int64{512, 4096, 65536} {
			out, _, err := appendRuns(nil, 2, []vcnRun{{NextVCN: 5, LCN: 9}}, cs)
			if err != nil || len(out) != 1 {
				t.Fatalf("cs=%d: %v", cs, err)
			}
			if out[0].FileOffset != 2*cs || out[0].VolumeOffset != 9*cs || out[0].Length != 3*cs {
				t.Errorf("cs=%d: %+v", cs, out[0])
			}
		}
	})

	t.Run("all sparse yields no extents but advances", func(t *testing.T) {
		out, next, err := appendRuns(nil, 0, []vcnRun{{NextVCN: 100, LCN: -1}}, cs)
		if err != nil || len(out) != 0 || next != 100 {
			t.Errorf("out=%+v next=%d err=%v", out, next, err)
		}
	})
}
