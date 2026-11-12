// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"testing"

	"github.com/google/uuid"
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

func readyLifecyclePlan(t *testing.T, s *Store, p models.PullRequest) models.Job {
	t.Helper()
	plan := claimJob(p, uuid.NewString())
	plan.Revision = p.HeadCommit
	Ok(t, s.StartJob(plan))
	Ok(t, s.StartPlan(plan.ID))
	snapshot := validSnapshot(plan.ID)
	Ok(t, s.BeginSnapshot(snapshot))
	Ok(t, s.CommitSnapshot(snapshot))
	Ok(t, s.FinishJob(plan.ID, "succeeded", ""))
	return plan
}

func TestPendingLifecycleVerificationBlocksJobAdmission(t *testing.T) {
	for _, kind := range []models.JobKind{models.CommandJob, models.WorkflowHookJob} {
		t.Run(string(kind), func(t *testing.T) {
			s := conformanceStore(t)
			p := claimPull()
			p.Execution = &models.Execution{Owner: "owner"}
			Ok(t, s.AcquireClaim(p, "owner"))
			plan := readyLifecyclePlan(t, s, p)
			queued := claimJob(p, uuid.NewString())
			queued.Command = "apply"
			queued.Kind = kind
			Ok(t, s.QueueLifecycleCheck(p))
			ErrContains(t, "awaiting VCS verification", s.StartJob(queued))
			recorded, err := s.Job(queued.ID)
			Ok(t, err)
			Assert(t, recorded == nil, "rejected job was persisted")
			pending, err := s.PendingPlans(p.BaseRepo, p.Num)
			Ok(t, err)
			Equals(t, 1, len(pending))
			Equals(t, plan.ID, pending[0].ID)
			Ok(t, s.CheckClaim(p))
			checks, err := s.PendingLifecycleChecks()
			Ok(t, err)
			Equals(t, 1, len(checks))
			Ok(t, s.ResolveLifecycleCheck(checks[0]))
			Ok(t, s.StartJob(queued))
			recorded, err = s.Job(queued.ID)
			Ok(t, err)
			Equals(t, "running", recorded.State)
			if kind == models.CommandJob {
				Equals(t, plan.ID, recorded.PlanJobID)
			}
			Ok(t, s.FinishJob(queued.ID, "succeeded", ""))
			Ok(t, s.ReleaseClaim(p))
		})
	}
}

func TestPendingLifecycleVerificationLetsAdmittedJobFinish(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	p.Execution = &models.Execution{Owner: "owner"}
	p.BaseRepo.Execution = p.Execution
	Ok(t, s.AcquireClaim(p, "owner"))
	readyLifecyclePlan(t, s, p)
	active := claimJob(p, uuid.NewString())
	active.Command = "apply"
	Ok(t, s.StartJob(active))
	acquired, _, err := s.TryLock(models.ProjectLock{Pull: p, Project: active.Project, Workspace: active.Workspace})
	Ok(t, err)
	Assert(t, acquired, "reservation missing")
	Ok(t, s.QueueLifecycleCheck(p))
	Ok(t, s.CheckClaim(p))
	Assert(t, s.ReleaseClaim(p) != nil, "unfinished job released ownership")
	Ok(t, s.MarkJobMutating(active.ID))
	Ok(t, s.FinishJob(active.ID, "succeeded", ""))
	recorded, err := s.Job(active.ID)
	Ok(t, err)
	Equals(t, "succeeded", recorded.State)
	reservation, err := s.GetLockInRepo(p.BaseRepo, active.Project, active.Workspace)
	Ok(t, err)
	Assert(t, reservation != nil, "finishing an active job released its project reservation")
	Ok(t, s.CheckClaim(p))
	Ok(t, s.ReleaseClaim(p))
	ErrContains(t, "awaiting VCS verification", s.AcquireClaim(p, "next-command"))
}
