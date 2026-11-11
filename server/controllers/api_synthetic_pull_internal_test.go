// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"math"
	"testing"
)

// Synthetic pull numbers key durable records, so they must stay negative and
// must not repeat independently of the clock.
func TestNextNonPRPullNumIsNegative(t *testing.T) {
	for range 128 {
		if n := nextNonPRPullNum(); n >= 0 {
			t.Fatalf("synthetic pull number %d is not negative", n)
		}
	}
}

func TestSyntheticPullNumStaysNegativeAtBounds(t *testing.T) {
	for _, random := range []uint64{0, 1, math.MaxUint64, 1 << 63, uint64(math.MaxInt), uint64(math.MaxInt) + 1} {
		if n := syntheticPullNum(random); n >= 0 {
			t.Fatalf("syntheticPullNum(%#x) = %d, want negative", random, n)
		}
	}
}
