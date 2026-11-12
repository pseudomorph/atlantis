// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/db"
	. "github.com/runatlantis/atlantis/testing"
	"gorm.io/gorm"
)

// Under READ COMMITTED, ReleaseClaim's count of running jobs would not see a
// job that StartJob has inserted but not committed. Both lock the governing
// pull row first, so exactly one wins each race: a job is never left running
// without the claim that admitted it.
func TestReleaseAndJobAdmissionSerialize(t *testing.T) {
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) {
		s := d.Store(t)
		for round := range 40 {
			p := claimPull()
			p.Num = round + 1
			p = claimed(t, s, p)
			j := claimJob(p, uuid.NewString())
			var released, started error
			var ready, wg sync.WaitGroup
			ready.Add(2)
			start := make(chan struct{})
			wg.Go(func() { ready.Done(); <-start; released = s.ReleaseClaim(p) })
			wg.Go(func() { ready.Done(); <-start; started = s.StartJob(j) })
			ready.Wait()
			close(start)
			wg.Wait()
			Assert(t, (released == nil) != (started == nil), "round %d: release %v, start %v", round, released, started)
			var running, claims int64
			Ok(t, s.db.Model(&job{}).Where("id = ? AND state = ?", j.ID, "running").Count(&running).Error)
			Ok(t, s.db.Model(&executionClaim{}).Where("owner = ?", p.ExecutionOwner()).Count(&claims).Error)
			if started == nil {
				Equals(t, int64(1), claims)
				Ok(t, s.FinishJob(j.ID, "succeeded", ""))
				Ok(t, s.ReleaseClaim(p))
			} else {
				Assert(t, errors.Is(started, db.ErrExecutionClaimLost), "round %d: unexpected admission error %v", round, started)
				Equals(t, int64(0), running)
				Equals(t, int64(0), claims)
			}
		}
	})
}

// Two owners race for the same pull: one acquires the claim, the other is
// refused, and the loser never sees a claim that is half written.
func TestConcurrentClaimsOnOnePull(t *testing.T) {
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) {
		s := d.Store(t)
		p := claimPull()
		const owners = 8
		errs := make([]error, owners)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range owners {
			wg.Go(func() { <-start; errs[i] = s.AcquireClaim(p, fmt.Sprintf("owner-%d", i)) })
		}
		close(start)
		wg.Wait()
		winners := 0
		for _, err := range errs {
			if err == nil {
				winners++
			} else {
				ErrContains(t, "already owned or uncertain", err)
			}
		}
		Equals(t, 1, winners)
	})
}

// A transaction waiting on the pull row lock sees the state committed by the
// holder, not the state from before it waited.
func TestRowLockWaitSeesCommittedClosure(t *testing.T) {
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) {
		s := d.Store(t)
		p := claimPull()
		Ok(t, s.ClosePull(p))
		Ok(t, s.MarkClosedClean(p))
		Ok(t, s.RequestReopen(p))
		holder := s.db.Begin()
		Ok(t, holder.Error)
		// Runs before the store closes, which waits for the open transaction.
		t.Cleanup(func() { holder.Rollback() })
		locked, err := lockPull(holder, pullIDs(holder, p.BaseRepo, p.Num))
		Ok(t, err)
		acquired := make(chan error, 1)
		go func() { acquired <- s.AcquireClaim(p, "after-wait") }()
		select {
		case err := <-acquired:
			t.Fatalf("claim did not wait for the pull row lock: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
		// The holder closes the pull again, which cancels the reopen request.
		Ok(t, holder.Model(&pull{}).Where("id = ?", locked.ID).Updates(map[string]any{"reopen_requested": false, "closure_generation": gorm.Expr("closure_generation + 1"), "cleanup_complete": false}).Error)
		Ok(t, holder.Commit().Error)
		ErrContains(t, "pull request is closed", <-acquired)
		idle, err := s.PullIdle(p)
		Ok(t, err)
		Assert(t, idle, "claim was acquired from state read before the lock wait")
	})
}
