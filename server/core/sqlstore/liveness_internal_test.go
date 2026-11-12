// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func storedClaim(t *testing.T, s *Store, owner string) executionClaim {
	t.Helper()
	var claim executionClaim
	Ok(t, s.db.Where("owner = ?", owner).Take(&claim).Error)
	return claim
}

// staleClaim moves a claim's heartbeat past the expiry, as if its server had
// stopped refreshing it, and attributes it to another instance.
func staleClaim(t *testing.T, s *Store, owner, instance string) {
	t.Helper()
	old := databaseTime(time.Now().Add(-s.claimExpiry - time.Minute))
	result := s.db.Model(&executionClaim{}).Where("owner = ?", owner).Updates(map[string]any{"heartbeat_at": old, "server_instance_id": instance})
	Ok(t, result.Error)
	Equals(t, int64(1), result.RowsAffected)
}

func TestServerInstancesAreDistinct(t *testing.T) {
	dir := t.TempDir()
	first, err := OpenServer(dir)
	Ok(t, err)
	Assert(t, first.InstanceID() != "", "store has no instance ID")
	Ok(t, first.Close())
	second, err := OpenServer(dir)
	Ok(t, err)
	defer second.Close()
	Assert(t, first.InstanceID() != second.InstanceID(), "restart reused instance %q", first.InstanceID())
}

func TestClaimsAndJobsRecordInstance(t *testing.T) {
	s := conformanceStore(t)
	p := claimed(t, s, claimPull())
	claim := storedClaim(t, s, p.ExecutionOwner())
	Equals(t, s.InstanceID(), claim.ServerInstanceID)
	Assert(t, claim.HeartbeatAt.Equal(claim.AcquiredAt), "first heartbeat %s differs from acquisition %s", claim.HeartbeatAt, claim.AcquiredAt)
	j := claimJob(p, uuid.NewString())
	Ok(t, s.StartJob(j))
	stored, err := s.Job(j.ID)
	Ok(t, err)
	Equals(t, s.InstanceID(), stored.ServerInstance)
}

func TestHeartbeatRefreshesOnlyThisInstancesActiveClaims(t *testing.T) {
	s := conformanceStore(t)
	mine := claimed(t, s, claimPull())
	other := claimPull()
	other.Num++
	other = claimed(t, s, other)
	uncertain := claimPull()
	uncertain.Num += 2
	uncertain = claimed(t, s, uncertain)
	old := databaseTime(time.Now().Add(-time.Hour))
	Ok(t, s.db.Model(&executionClaim{}).Where("1 = 1").Update("heartbeat_at", old).Error)
	Ok(t, s.db.Model(&executionClaim{}).Where("owner = ?", other.ExecutionOwner()).Update("server_instance_id", "other-instance").Error)
	Ok(t, s.MarkUncertain(uncertain))

	refreshed, err := s.RefreshClaimHeartbeats()
	Ok(t, err)
	Equals(t, int64(1), refreshed)
	Assert(t, storedClaim(t, s, mine.ExecutionOwner()).HeartbeatAt.After(old), "own claim was not refreshed")
	Assert(t, storedClaim(t, s, other.ExecutionOwner()).HeartbeatAt.Equal(old), "another instance's claim was refreshed")
	got := storedClaim(t, s, uncertain.ExecutionOwner())
	Assert(t, got.HeartbeatAt.Equal(old) && got.State == "uncertain", "uncertain claim was revived: %+v", got)
}

func TestExpirySweepMarksStaleClaimsUncertainAndNeverTransfers(t *testing.T) {
	s := conformanceStore(t)
	live := claimed(t, s, claimPull())
	stale := claimPull()
	stale.Num++
	stale = claimed(t, s, stale)
	liveJob, staleJob := claimJob(live, uuid.NewString()), claimJob(stale, uuid.NewString())
	Ok(t, s.StartJob(liveJob))
	Ok(t, s.StartJob(staleJob))
	Ok(t, s.AppendLogChunk(staleJob.ID, []byte("partial")))
	staleClaim(t, s, stale.ExecutionOwner(), "crashed-instance")

	expired, err := s.ExpireClaims()
	Ok(t, err)
	Equals(t, 1, expired)

	// The live claim keeps its state and its job keeps writing.
	Ok(t, s.CheckClaim(live))
	Ok(t, s.AppendLogChunk(liveJob.ID, []byte("still running")))
	stream, err := s.Log(liveJob.ID)
	Ok(t, err)
	Assert(t, stream.SealedAt == nil, "live job log was sealed")

	// The stale claim is uncertain with an interrupted log, and its job stays
	// running until an operator reconciles it.
	got := storedClaim(t, s, stale.ExecutionOwner())
	Equals(t, "uncertain", got.State)
	Equals(t, "crashed-instance", got.ServerInstanceID)
	Assert(t, errors.Is(s.CheckClaim(stale), db.ErrExecutionClaimLost), "expired claim still authorizes writes")
	stream, err = s.Log(staleJob.ID)
	Ok(t, err)
	Assert(t, stream.SealedAt != nil && stream.Interrupted, "stale job log was not sealed as interrupted: %+v", stream)
	var running job
	Ok(t, s.db.Where("id = ?", staleJob.ID).Take(&running).Error)
	Equals(t, "running", running.State)
	ErrContains(t, "already owned or uncertain", s.AcquireClaim(stale, uuid.NewString()))
	Ok(t, s.ClosePull(stale))
	_, claimedCleanup, err := s.ClaimCleanup(stale, "cleanup")
	ErrContains(t, "cleanup deferred", err)
	Assert(t, !claimedCleanup, "cleanup took over an expired claim")

	// A later heartbeat from the old owner cannot revive it, and a second sweep is a no-op.
	Ok(t, s.db.Model(&executionClaim{}).Where("owner = ?", stale.ExecutionOwner()).Update("server_instance_id", s.InstanceID()).Error)
	_, err = s.RefreshClaimHeartbeats()
	Ok(t, err)
	Equals(t, "uncertain", storedClaim(t, s, stale.ExecutionOwner()).State)
	expired, err = s.ExpireClaims()
	Ok(t, err)
	Equals(t, 0, expired)
}

func TestLiveClaimIsNeverSwept(t *testing.T) {
	s := conformanceStore(t)
	s.claimExpiry = 500 * time.Millisecond
	p := claimed(t, s, claimPull())
	j := claimJob(p, uuid.NewString())
	Ok(t, s.StartJob(j))
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	errs := make(chan error, 2)
	loop := func(fn func() error, every time.Duration) {
		defer wg.Done()
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if err := fn(); err != nil {
					errs <- err
					return
				}
			}
		}
	}
	go loop(func() error { _, err := s.RefreshClaimHeartbeats(); return err }, 20*time.Millisecond)
	go loop(func() error {
		n, err := s.ExpireClaims()
		if err == nil && n != 0 {
			err = errors.New("sweep expired a live claim")
		}
		return err
	}, 5*time.Millisecond)
	time.Sleep(3 * s.claimExpiry)
	close(stop)
	wg.Wait()
	close(errs)
	for err := range errs {
		Ok(t, err)
	}
	Ok(t, s.CheckClaim(p))
	Ok(t, s.FinishJob(j.ID, "succeeded", ""))
	Ok(t, s.ReleaseClaim(p))
}

func TestRecoveryOfExpiredClaimRecordsPreviousInstance(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	p.Execution = &models.Execution{Owner: "crashed"}
	Ok(t, s.AcquireClaim(p, "crashed"))
	j := claimJob(p, uuid.NewString())
	Ok(t, s.StartJob(j))
	Ok(t, s.StartPlan(j.ID))

	// The claim is active in the database, but its heartbeat has expired and no
	// sweep has run yet. Recovery treats it as uncertain and still requires attestation.
	staleClaim(t, s, "crashed", "crashed-instance")
	ErrContains(t, "--confirm-stopped", s.AcquireUnlockClaim(p, "recovery", "operator", false))
	Ok(t, s.AcquireUnlockClaim(p, "recovery", "operator", true))

	got := storedClaim(t, s, "recovery")
	Equals(t, "active", got.State)
	Equals(t, s.InstanceID(), got.ServerInstanceID)
	Assert(t, got.HeartbeatAt.Equal(got.AcquiredAt), "recovery claim has a stale heartbeat")
	var audit struct{ PreviousOwner, PreviousServerInstanceID, RecoveryServerInstanceID, Actor string }
	Ok(t, s.db.Table("execution_recoveries").Take(&audit).Error)
	Equals(t, "crashed", audit.PreviousOwner)
	Equals(t, "crashed-instance", audit.PreviousServerInstanceID)
	Equals(t, s.InstanceID(), audit.RecoveryServerInstanceID)
	Equals(t, "operator", audit.Actor)
	var interrupted job
	Ok(t, s.db.Where("id = ?", j.ID).Take(&interrupted).Error)
	Equals(t, "interrupted", interrupted.State)
	stream, err := s.Log(j.ID)
	Ok(t, err)
	Assert(t, stream.SealedAt != nil && stream.Interrupted, "recovered job log was not sealed as interrupted")
	var pending int64
	Ok(t, s.db.Table("pending_plans").Count(&pending).Error)
	Equals(t, int64(0), pending)
}

func TestAPIRecoveryOfSweptClaim(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	p.Num = -3
	p.Execution = &models.Execution{Owner: "original"}
	Ok(t, s.AcquireClaim(p, "original"))
	source := claimJob(p, uuid.NewString())
	Ok(t, s.StartJob(source))
	staleClaim(t, s, "original", "crashed-instance")
	expired, err := s.ExpireClaims()
	Ok(t, err)
	Equals(t, 1, expired)

	recovered, err := s.AcquireAPIJobUnlock(source.ID, "recovery", "api-token", true)
	Ok(t, err)
	Ok(t, s.ReleaseClaim(recovered))
	var audit struct{ PreviousOwner, PreviousServerInstanceID, RecoveryServerInstanceID string }
	Ok(t, s.db.Table("execution_recoveries").Take(&audit).Error)
	Equals(t, "original", audit.PreviousOwner)
	Equals(t, "crashed-instance", audit.PreviousServerInstanceID)
	Equals(t, s.InstanceID(), audit.RecoveryServerInstanceID)
}

func TestStartupMarksEarlierInstancesUncertain(t *testing.T) {
	dir := t.TempDir()
	first, err := OpenServer(dir)
	Ok(t, err)
	p := claimed(t, first, claimPull())
	Ok(t, first.Close())

	second, err := OpenServer(dir)
	Ok(t, err)
	defer second.Close()
	mine := claimPull()
	mine.Num++
	mine = claimed(t, second, mine)
	Ok(t, second.MarkPreviousExecutionsUncertain())
	earlier := storedClaim(t, second, p.ExecutionOwner())
	Equals(t, "uncertain", earlier.State)
	Equals(t, first.InstanceID(), earlier.ServerInstanceID)
	Ok(t, second.CheckClaim(mine))
}
