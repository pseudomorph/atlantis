// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func conformanceStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenServer(t.TempDir())
	Ok(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func claimPull() models.PullRequest {
	repo := models.Repo{FullName: "team/infra", Owner: "team", Name: "infra", CloneURL: "https://git.example:8443/gitlab/team/infra.git", VCSHost: models.VCSHost{Type: models.Gitlab, Hostname: "git.example"}}
	return models.PullRequest{Num: 17, BaseRepo: repo, HeadCommit: "revision", HeadBranch: "feature", BaseBranch: "main"}
}
func claimJob(p models.PullRequest, id string) models.Job {
	return models.Job{ID: id, Pull: p, Project: models.NewProject(p.BaseRepo.FullName, ".", ""), Workspace: "default", Command: "plan", State: "running", StartedAt: time.Now().UTC()}
}
func TestExecutionDatabaseConformance(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		t.Run("unfinished ownership blocks reuse", func(t *testing.T) {
			dir := t.TempDir()
			s, err := OpenServer(dir)
			Ok(t, err)
			t.Cleanup(func() { _ = s.Close() })
			p := claimPull()
			Ok(t, s.AcquireClaim(p, "owner-a"))
			p.Execution = &models.Execution{Owner: "owner-a"}
			p.BaseRepo.Execution = p.Execution
			Assert(t, s.AcquireClaim(p, "owner-b") != nil, "duplicate claim acquired")
			j := claimJob(p, uuid.NewString())
			Ok(t, s.StartJob(j))
			Ok(t, s.StartPlan(j.ID))
			projectLock := models.ProjectLock{Pull: p, Project: j.Project, Workspace: j.Workspace, Time: time.Now().UTC()}
			acquired, _, err := s.TryLock(projectLock)
			Ok(t, err)
			Assert(t, acquired, "project lock unavailable")
			Ok(t, s.Close())
			s, err = OpenServer(dir)
			Ok(t, err)
			Ok(t, s.MarkPreviousExecutionsUncertain())
			Assert(t, s.FinishJob(j.ID, "succeeded", "") != nil, "uncertain owner published completion")
			Assert(t, s.AcquireClaim(p, "owner-b") != nil, "unfinished work allowed takeover")
			locked, err := s.GetLock(p.BaseRepo, j.Project, j.Workspace)
			Ok(t, err)
			Assert(t, locked != nil, "startup removed user project lock")
		})
		t.Run("closure and cancellation do not release execution", func(t *testing.T) {
			s := conformanceStore(t)
			p := claimPull()
			Ok(t, s.AcquireClaim(p, "owner"))
			p.Execution = &models.Execution{Owner: "owner"}
			j := claimJob(p, uuid.NewString())
			Ok(t, s.StartJob(j))
			Ok(t, s.ClosePull(p))
			Ok(t, s.StartPlan(j.ID))
			a := models.Artifact{ID: uuid.NewString(), JobID: j.ID, Kind: "plan", RelativePath: "plan.tfplan", Location: "closed/plan", CreatedAt: time.Now().UTC()}
			Ok(t, s.BeginArtifact(a))
			Ok(t, s.CommitArtifact(a))
			idle, err := s.PullIdle(p)
			Ok(t, err)
			Assert(t, !idle, "closure discarded execution ownership")
			Assert(t, s.AcquireClaim(p, "contender") != nil, "closed pull accepted execution")
			Ok(t, s.FinishJob(j.ID, "succeeded", ""))
			got, err := s.Job(j.ID)
			Ok(t, err)
			Equals(t, "succeeded", got.State)
			pending, err := s.PendingPlans(p.BaseRepo, p.Num)
			Ok(t, err)
			Equals(t, 0, len(pending))
			Ok(t, s.ReleaseClaim(p))
			idle, err = s.PullIdle(p)
			Ok(t, err)
			Assert(t, idle, "stopped execution remained active")
		})
		t.Run("cancellation preserves active outcomes and blocks queued jobs", func(t *testing.T) {
			s := conformanceStore(t)
			p := claimPull()
			Ok(t, s.AcquireClaim(p, "owner"))
			p.Execution = &models.Execution{Owner: "owner"}
			active := claimJob(p, uuid.NewString())
			Ok(t, s.StartJob(active))
			Ok(t, s.StartPlan(active.ID))
			Ok(t, s.CancelPull(p))
			Assert(t, s.StartJob(claimJob(p, uuid.NewString())) != nil, "queued job started after cancellation")
			Assert(t, s.ReleaseClaim(p) != nil, "cancellation released unfinished work")
			Ok(t, s.CheckClaim(p))
			a := models.Artifact{ID: uuid.NewString(), JobID: active.ID, Kind: "plan", RelativePath: "plan.tfplan", Location: "active/plan", CreatedAt: time.Now().UTC()}
			Ok(t, s.BeginArtifact(a))
			Ok(t, s.CommitArtifact(a))
			Ok(t, s.FinishJob(active.ID, "succeeded", ""))
			got, err := s.Job(active.ID)
			Ok(t, err)
			Equals(t, "succeeded", got.State)
			pending, err := s.PendingPlans(p.BaseRepo, p.Num)
			Ok(t, err)
			Equals(t, 1, len(pending))
			Ok(t, s.ReleaseClaim(p))
			Ok(t, s.AcquireClaim(p, "next-owner"))
			p.Execution = &models.Execution{Owner: "next-owner"}
			cancelled, err := s.IsCancelled(p)
			Ok(t, err)
			Assert(t, !cancelled, "new execution inherited prior cancellation")
		})
		t.Run("publication requires intent and live ownership", func(t *testing.T) {
			s := conformanceStore(t)
			p := claimPull()
			Assert(t, s.StartJob(claimJob(p, uuid.NewString())) != nil, "unclaimed job accepted")
			Ok(t, s.AcquireClaim(p, "owner"))
			p.Execution = &models.Execution{Owner: "owner"}
			j := claimJob(p, uuid.NewString())
			Ok(t, s.StartJob(j))
			a := models.Artifact{ID: uuid.NewString(), JobID: j.ID, Kind: "plan", Location: "test/plan", RelativePath: "plan.tfplan", CreatedAt: time.Now().UTC()}
			Assert(t, s.CommitArtifact(a) != nil, "publication without intent accepted")
			Ok(t, s.BeginArtifact(a))
			rows, err := s.Artifacts(j.ID)
			Ok(t, err)
			Equals(t, 0, len(rows))
			Ok(t, s.MarkUncertain(p))
			Assert(t, s.CommitArtifact(a) != nil, "uncertain owner published artifact")
			rows, err = s.Artifacts(j.ID)
			Ok(t, err)
			Equals(t, 0, len(rows))
		})
		t.Run("cancellation invalidates mutating apply input", func(t *testing.T) {
			s := conformanceStore(t)
			p := claimPull()
			Ok(t, s.AcquireClaim(p, "owner"))
			p.Execution = &models.Execution{Owner: "owner"}
			producer := claimJob(p, uuid.NewString())
			Ok(t, s.StartJob(producer))
			Ok(t, s.StartPlan(producer.ID))
			a := models.Artifact{ID: uuid.NewString(), JobID: producer.ID, Kind: "plan", RelativePath: "plan.tfplan", Location: "plan/object", CreatedAt: time.Now().UTC()}
			Ok(t, s.BeginArtifact(a))
			Ok(t, s.CommitArtifact(a))
			Ok(t, s.FinishJob(producer.ID, "succeeded", ""))
			consumer := claimJob(p, uuid.NewString())
			consumer.Command = "apply"
			Ok(t, s.StartJob(consumer))
			Ok(t, s.MarkJobMutating(consumer.ID))
			Ok(t, s.CancelPull(p))
			Ok(t, s.FinishJob(consumer.ID, "failed", "process cancelled"))
			pending, err := s.PendingPlans(p.BaseRepo, p.Num)
			Ok(t, err)
			Equals(t, 0, len(pending))
		})
		t.Run("API plan inputs end with their invocation", func(t *testing.T) {
			s := conformanceStore(t)
			p := claimPull()
			p.Num = -42
			p.Execution = &models.Execution{Owner: "api-owner"}
			Ok(t, s.AcquireClaim(p, p.ExecutionOwner()))
			j := claimJob(p, uuid.NewString())
			Ok(t, s.StartJob(j))
			Ok(t, s.StartPlan(j.ID))
			a := models.Artifact{ID: uuid.NewString(), JobID: j.ID, Kind: "plan", Location: "api/plan", CreatedAt: time.Now().UTC()}
			Ok(t, s.BeginArtifact(a))
			Ok(t, s.CommitArtifact(a))
			Ok(t, s.FinishJob(j.ID, "succeeded", ""))
			pending, err := s.PendingPlans(p.BaseRepo, p.Num)
			Ok(t, err)
			Equals(t, 1, len(pending))
			Ok(t, s.ReleaseClaim(p))
			pending, err = s.PendingPlans(p.BaseRepo, p.Num)
			Ok(t, err)
			Equals(t, 0, len(pending))
			history, err := s.Job(j.ID)
			Ok(t, err)
			Equals(t, "succeeded", history.State)
		})
		t.Run("pull identity and foreign keys", func(t *testing.T) {
			s := conformanceStore(t)
			p := claimPull()
			Ok(t, s.AcquireClaim(p, "owner"))
			p.Execution = &models.Execution{Owner: "owner"}
			other := p
			other.BaseRepo.CloneURL = "https://other.example/gitlab/team/infra.git"
			Assert(t, s.CheckClaim(other) != nil, "claim crossed VCS instances")
			other = p
			other.Num++
			Assert(t, s.CheckClaim(other) != nil, "claim crossed pull requests")
			Assert(t, s.db.Exec("INSERT INTO projects(id,repository_id,directory,name,workspace) VALUES ('project','missing','.','','default')").Error != nil, "missing foreign key accepted")
			var claim executionClaim
			Ok(t, s.db.First(&claim).Error)
			var stored pull
			Ok(t, s.db.First(&stored).Error)
			Equals(t, stored.ID, claim.PullID)
			Ok(t, s.ReleaseClaim(p))
		})
	})
}
