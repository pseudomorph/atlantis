// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"errors"
	"testing"

	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestCleanupOwnershipAndGenerationFence(t *testing.T) {
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) {
		s := d.Store(t)
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
	})
}

func TestFixedPullAndClaimInvariants(t *testing.T) {
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) {
		s := d.Store(t)
		p := claimPull()
		p.Execution = &models.Execution{Owner: "owner"}
		Ok(t, s.AcquireClaim(p, "owner"))
		for _, statement := range []string{
			"UPDATE pulls SET closed=TRUE",
			"UPDATE pulls SET cleanup_complete=TRUE",
			"UPDATE pulls SET reopen_requested=TRUE",
			"UPDATE pulls SET closure_generation=-1",
			"UPDATE execution_claims SET owner=''",
			"INSERT INTO pending_pull_lifecycles(pull_id,generation) SELECT id,0 FROM pulls",
		} {
			checkViolation(t, s.db.Exec(statement).Error)
		}
		Ok(t, s.CheckClaim(p))
		Ok(t, s.ClosePull(p))
		Ok(t, s.ReleaseClaim(p))
		Ok(t, s.MarkClosedClean(p))
		Ok(t, s.RequestReopen(p))
		Ok(t, s.AcquireClaim(p, "reopened"))
	})
}

func TestSyntheticCollisionDoesNotOverwriteHistory(t *testing.T) {
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) {
		for _, state := range []string{"active", "uncertain", "released"} {
			t.Run(state, func(t *testing.T) {
				d := d.Fresh(t)
				s := d.Store(t)
				p := claimPull()
				p.Num = -17
				p.Execution = &models.Execution{Owner: "first"}
				Ok(t, s.AcquireClaim(p, "first"))
				if state == "active" {
					_, err := s.UpdatePullWithResults(p, nil)
					Ok(t, err)
				}
				if state == "uncertain" {
					Ok(t, s.MarkUncertain(p))
				}
				if state == "released" {
					Ok(t, s.ReleaseClaim(p))
				}
				other := p
				other.HeadCommit = "replacement"
				other.Execution = &models.Execution{Owner: "next"}
				Assert(t, errors.Is(s.AcquireClaim(other, "next"), db.ErrSyntheticPullCollision), "historical API identity was reused")
				var stored pull
				Ok(t, s.db.First(&stored).Error)
				Equals(t, p.HeadCommit, stored.HeadCommit)
				var count int64
				Ok(t, s.db.Table("pulls").Count(&count).Error)
				Equals(t, int64(1), count)
			})
		}
	})
}
