// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"testing"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestUnlockRecoveryRequiresStoppedAttestation(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	p.Execution = &models.Execution{Owner: "crashed"}
	Ok(t, s.AcquireClaim(p, "crashed"))
	j := claimJob(p, uuid.NewString())
	Ok(t, s.StartJob(j))
	Ok(t, s.StartPlan(j.ID))
	lock := models.ProjectLock{Pull: p, Project: j.Project, Workspace: j.Workspace}
	acquired, _, err := s.TryLock(lock)
	Ok(t, err)
	Assert(t, acquired, "reservation missing")
	ErrContains(t, "execution is active", s.AcquireUnlockClaim(p, "recovery", "operator", true))
	Ok(t, s.MarkUncertain(p))
	ErrContains(t, "--confirm-stopped", s.AcquireUnlockClaim(p, "recovery", "operator", false))
	var old job
	Ok(t, s.db.Where("id = ?", j.ID).Take(&old).Error)
	Equals(t, "running", old.State)
	Ok(t, s.AcquireUnlockClaim(p, "recovery", "operator", true))
	p.Execution = &models.Execution{Owner: "recovery"}
	p.BaseRepo.Execution = p.Execution
	Ok(t, s.CheckClaim(p))
	Ok(t, s.db.Where("id = ?", j.ID).Take(&old).Error)
	Equals(t, "interrupted", old.State)
	Assert(t, old.FinishedAt != nil, "interrupted job lacks completion time")
	var pending int64
	Ok(t, s.db.Table("pending_plans").Count(&pending).Error)
	Equals(t, int64(0), pending)
	var actor string
	Ok(t, s.db.Table("execution_recoveries").Select("actor").Scan(&actor).Error)
	Equals(t, "operator", actor)
	reservation, err := s.GetLockInRepo(p.BaseRepo, j.Project, j.Workspace)
	Ok(t, err)
	Assert(t, reservation != nil, "reconciliation released reservation before unlock")
	_, err = s.UnlockByPullInRepo(p.BaseRepo, p.Num)
	Ok(t, err)
	Ok(t, s.ReleaseClaim(p))
	Ok(t, s.AcquireClaim(p, "fresh-plan"))
}

func TestFailedRecoveryIsAtomic(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	p.Execution = &models.Execution{Owner: "crashed"}
	Ok(t, s.AcquireClaim(p, "crashed"))
	j := claimJob(p, uuid.NewString())
	Ok(t, s.StartJob(j))
	Ok(t, s.MarkUncertain(p))
	Ok(t, s.db.Exec("CREATE TRIGGER reject_recovery BEFORE INSERT ON execution_recoveries BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END").Error)
	Assert(t, s.AcquireUnlockClaim(p, "recovery", "operator", true) != nil, "failed audit accepted")
	var existing executionClaim
	Ok(t, s.db.First(&existing).Error)
	Equals(t, "crashed", existing.Owner)
	Equals(t, "uncertain", existing.State)
	var stored job
	Ok(t, s.db.Where("id = ?", j.ID).Take(&stored).Error)
	Equals(t, "running", stored.State)
}

func TestAPIRecoveryAuditAndInterruptedRecoveryRetry(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	p.Num = -7
	p.Execution = &models.Execution{Owner: "original"}
	Ok(t, s.AcquireClaim(p, "original"))
	first, second := claimJob(p, uuid.NewString()), claimJob(p, uuid.NewString())
	Ok(t, s.StartJob(first))
	Ok(t, s.StartJob(second))
	_, err := s.AcquireAPIJobUnlock(first.ID, "r1", "api-token", true)
	ErrContains(t, "active", err)
	var audits int64
	Ok(t, s.db.Table("execution_recoveries").Count(&audits).Error)
	Equals(t, int64(0), audits)
	Ok(t, s.MarkUncertain(p))
	recovered, err := s.AcquireAPIJobUnlock(first.ID, "r1", "api-token", true)
	Ok(t, err)
	Ok(t, s.MarkUncertain(recovered))
	recovered, err = s.AcquireAPIJobUnlock(second.ID, "r2", "api-token", true)
	Ok(t, err)
	Ok(t, s.ReleaseClaim(recovered))
	recovered, err = s.AcquireAPIJobUnlock(first.ID, "r3", "api-token", true)
	Ok(t, err)
	Ok(t, s.ReleaseClaim(recovered))
	var rows []struct{ Actor, PreviousOwner, RecoveryOwner, SourceJobID string }
	Ok(t, s.db.Table("execution_recoveries").Order("recovered_at, recovery_owner").Find(&rows).Error)
	Equals(t, 3, len(rows))
	for _, row := range rows {
		Equals(t, "api-token", row.Actor)
		Assert(t, row.SourceJobID == first.ID || row.SourceJobID == second.ID, "audit lost source job")
	}
	Equals(t, "original", rows[0].PreviousOwner)
	Equals(t, "r1", rows[1].PreviousOwner)
	Equals(t, "original", rows[2].PreviousOwner)
}

func TestAPIRecoveryRefusesUnrelatedOwner(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	p.Num = -1
	p.Execution = &models.Execution{Owner: "original"}
	Ok(t, s.AcquireClaim(p, "original"))
	source := claimJob(p, uuid.NewString())
	Ok(t, s.StartJob(source))
	Ok(t, s.MarkUncertain(p))
	Ok(t, s.db.Model(&executionClaim{}).Where("owner = ?", "original").Update("owner", "unrelated").Error)
	_, err := s.AcquireAPIJobUnlock(source.ID, "recovery", "api-token", true)
	ErrContains(t, "older execution owner", err)
	var claim executionClaim
	Ok(t, s.db.First(&claim).Error)
	Equals(t, "unrelated", claim.Owner)
	Equals(t, "uncertain", claim.State)
	var count int64
	Ok(t, s.db.Table("execution_recoveries").Count(&count).Error)
	Equals(t, int64(0), count)
}

func TestRecoverySourceJobMustMatchPull(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	p.Execution = &models.Execution{Owner: "one"}
	Ok(t, s.AcquireClaim(p, "one"))
	first := claimJob(p, uuid.NewString())
	Ok(t, s.StartJob(first))
	p.Num++
	p.Execution = &models.Execution{Owner: "two"}
	Ok(t, s.AcquireClaim(p, "two"))
	second := claimJob(p, uuid.NewString())
	Ok(t, s.StartJob(second))
	var row job
	Ok(t, s.db.Where("id = ?", first.ID).Take(&row).Error)
	insert := func(source any) error {
		return s.db.Exec("INSERT INTO execution_recoveries(id,pull_id,previous_owner,recovery_owner,source_job_id,actor,recovered_at) VALUES (?,?,?,?,?,?,CURRENT_TIMESTAMP)", uuid.NewString(), row.PullID, "one", "recovery", source, "operator").Error
	}
	ErrContains(t, "FOREIGN KEY constraint failed", insert(second.ID))
	Ok(t, insert(first.ID))
	Ok(t, insert(nil))
}
