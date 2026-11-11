// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"testing"

	"github.com/runatlantis/atlantis/server/events/models"

	. "github.com/runatlantis/atlantis/testing"
)

func TestSecondCloseCancelsPendingReopen(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	Ok(t, s.ClosePull(p))
	Ok(t, s.RequestReopen(p))
	Ok(t, s.ClosePull(p))
	Ok(t, s.MarkClosedClean(p))
	ErrContains(t, "pull request is closed", s.AcquireClaim(p, "must-not-reopen"))
}

func TestLifecycleVerificationBlocksAdmissionAndFencesRetries(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	Ok(t, s.QueueLifecycleCheck(p))
	ErrContains(t, "awaiting VCS verification", s.AcquireClaim(p, "before-verification"))
	pending, err := s.PendingLifecycleChecks()
	Ok(t, err)
	Equals(t, 1, len(pending))
	first := pending[0]
	Ok(t, s.QueueLifecycleCheck(p))
	Ok(t, s.ResolveLifecycleCheck(first))
	pending, err = s.PendingLifecycleChecks()
	Ok(t, err)
	Equals(t, 1, len(pending))
	Assert(t, pending[0].LifecycleGeneration > first.LifecycleGeneration, "old verification cleared a newer retry")
	Ok(t, s.ResolveLifecycleCheck(pending[0]))
	Ok(t, s.AcquireClaim(p, "verified-open"))
}

func TestLifecycleVerificationStopsQueuedProjectsWithoutLosingOwnership(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	p.Execution = &models.Execution{Owner: "active"}
	Ok(t, s.AcquireClaim(p, p.ExecutionOwner()))
	Ok(t, s.QueueLifecycleCheck(p))
	cancelled, err := s.IsCancelled(p)
	Ok(t, err)
	Assert(t, cancelled, "queued projects can start before closure verification")
	Ok(t, s.CheckClaim(p))
	pending, err := s.PendingLifecycleChecks()
	Ok(t, err)
	Ok(t, s.ResolveLifecycleCheck(pending[0]))
	cancelled, err = s.IsCancelled(p)
	Ok(t, err)
	Assert(t, !cancelled, "verified open pull stayed cancelled")
	Ok(t, s.ReleaseClaim(p))
}
