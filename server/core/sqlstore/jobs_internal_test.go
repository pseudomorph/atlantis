// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"testing"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestPlanBindingConstraints(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	p.Execution = &models.Execution{Owner: "owner"}
	Ok(t, s.AcquireClaim(p, "owner"))
	command := claimJob(p, uuid.NewString())
	Ok(t, s.StartJob(command))
	var row job
	Ok(t, s.db.Where("id = ?", command.ID).Take(&row).Error)
	ErrContains(t, "CHECK constraint failed", s.db.Create(&planBinding{JobID: row.ID, PlanJobID: row.ID, ProjectID: *row.ProjectID, PullID: row.PullID}).Error)
	Ok(t, s.FinishJob(command.ID, "succeeded", ""))
}

func TestAdmissionRejectsInvalidReadyProducer(t *testing.T) {
	for _, kind := range []string{"apply", "unfinished-plan"} {
		t.Run(kind, func(t *testing.T) {
			s := conformanceStore(t)
			p := claimPull()
			p.Execution = &models.Execution{Owner: "owner"}
			Ok(t, s.AcquireClaim(p, "owner"))
			producer := claimJob(p, uuid.NewString())
			if kind == "apply" {
				producer.Command = "apply"
			}
			Ok(t, s.StartJob(producer))
			if kind == "apply" {
				Ok(t, s.FinishJob(producer.ID, "succeeded", ""))
			}
			var row job
			Ok(t, s.db.Where("id = ?", producer.ID).Take(&row).Error)
			Ok(t, s.db.Create(&pendingPlan{ProjectID: *row.ProjectID, PullID: row.PullID, JobID: row.ID, Ready: true}).Error)
			consumer := claimJob(p, uuid.NewString())
			consumer.Command = "apply"
			ErrContains(t, "not a completed plan", s.StartJob(consumer))
			absent, err := s.Job(consumer.ID)
			Ok(t, err)
			Assert(t, absent == nil, "invalid producer admitted a consumer")
		})
	}
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
