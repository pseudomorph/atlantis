// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"testing"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestSchemaRejectsCrossPullRelationships(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	p.Execution = &models.Execution{Owner: "first"}
	Ok(t, s.AcquireClaim(p, "first"))
	j := claimJob(p, uuid.NewString())
	Ok(t, s.StartJob(j))
	var first job
	Ok(t, s.db.Where("id = ?", j.ID).Take(&first).Error)
	other := p
	other.BaseRepo.FullName = "team/other"
	other.Execution = &models.Execution{Owner: "second"}
	Ok(t, s.AcquireClaim(other, "second"))
	otherJob := claimJob(other, uuid.NewString())
	Ok(t, s.StartJob(otherJob))
	var second job
	Ok(t, s.db.Where("id = ?", otherJob.ID).Take(&second).Error)
	Assert(t, s.db.Exec("UPDATE jobs SET repository_id = ? WHERE id = ?", second.RepositoryID, first.ID).Error != nil, "job accepted another repository")
	Assert(t, s.db.Exec("UPDATE execution_claims SET pull_id = ? WHERE pull_id = ?", "missing", first.PullID).Error != nil, "claim accepted a missing pull")
	Assert(t, s.db.Exec("UPDATE jobs SET pull_id = ? WHERE id = ?", second.PullID, first.ID).Error != nil, "job accepted another repository's pull")
	Assert(t, s.db.Exec("UPDATE jobs SET plan_job_id = ? WHERE id = ?", second.ID, first.ID).Error != nil, "job accepted another project's plan")
	Assert(t, s.FinishJob(first.ID, "running", "") != nil, "running accepted as a terminal state")
	Assert(t, s.FinishJob(first.ID, "typo", "") != nil, "unknown terminal state accepted")
}

func TestCompletionWriteFailureRetainsProjectReservation(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	p.Execution = &models.Execution{Owner: "apply-owner"}
	Ok(t, s.AcquireClaim(p, p.ExecutionOwner()))
	j := claimJob(p, uuid.NewString())
	j.Command = "apply"
	Ok(t, s.StartJob(j))
	acquired, _, err := s.TryLock(models.ProjectLock{Pull: p, Project: j.Project, Workspace: j.Workspace})
	Ok(t, err)
	Assert(t, acquired, "apply reservation missing")
	Ok(t, s.db.Exec("CREATE TRIGGER reject_completion BEFORE UPDATE OF state ON jobs BEGIN SELECT RAISE(ABORT, 'database completion unavailable'); END").Error)
	Assert(t, s.FinishJob(j.ID, "succeeded", "") != nil, "completion failure was ignored")
	Assert(t, s.ReleaseClaim(p) != nil, "unknown outcome released its claim")
	Ok(t, s.MarkUncertain(p))
	other := p
	other.Num++
	other.Execution = &models.Execution{Owner: "other-pr"}
	Ok(t, s.AcquireClaim(other, other.ExecutionOwner()))
	acquired, _, err = s.TryLock(models.ProjectLock{Pull: other, Project: j.Project, Workspace: j.Workspace})
	Ok(t, err)
	Assert(t, !acquired, "another PR applied while the prior outcome was uncertain")
}
