// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"testing"

	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestCleanupOwnershipAndGenerationFence(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	Ok(t, s.ClosePull(p))
	ready, err := s.ClosedPullsReady()
	Ok(t, err)
	Equals(t, 1, len(ready))
	stale := ready[0]
	first, acquired, err := s.ClaimCleanup(stale, "cleaner-one")
	Ok(t, err)
	Assert(t, acquired, "cleanup was not admitted")
	_, _, err = s.ClaimCleanup(stale, "cleaner-two")
	Assert(t, err != nil, "concurrent cleanup admitted")
	Ok(t, s.RequestReopen(p))
	Ok(t, s.MarkClosedClean(first))
	Assert(t, s.AcquireClaim(p, "too-early") != nil, "command overlapped cleanup")
	Ok(t, s.ReleaseClaim(first))
	Ok(t, s.AcquireClaim(p, "reopened"))
	p.Execution = &models.Execution{Owner: "reopened"}
	Ok(t, s.ReleaseClaim(p))
	Ok(t, s.ClosePull(p))
	_, acquired, err = s.ClaimCleanup(stale, "late-cleaner")
	Ok(t, err)
	Assert(t, !acquired, "old closure cleaned a new generation")
	ready, err = s.ClosedPullsReady()
	Ok(t, err)
	Equals(t, 1, len(ready))
	Assert(t, ready[0].ClosureGeneration > stale.ClosureGeneration, "closure generation did not advance")
}
