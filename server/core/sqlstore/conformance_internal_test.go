// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"strings"
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
func validSnapshot(jobID string) models.PlanSnapshot {
	return models.PlanSnapshot{JobID: jobID, Size: 1, SHA256: strings.Repeat("a", 64), Manifest: models.SnapshotManifest{Version: 1, Files: []models.SnapshotFile{{Kind: "plan", Path: "plan.tfplan", Size: 1, SHA256: strings.Repeat("b", 64), Mode: 0600}}}}
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
			a := validSnapshot(j.ID)
			Ok(t, s.BeginSnapshot(a))
			Ok(t, s.CommitSnapshot(a))
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
			a := validSnapshot(active.ID)
			Ok(t, s.BeginSnapshot(a))
			Ok(t, s.CommitSnapshot(a))
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
			a := validSnapshot(j.ID)
			Assert(t, s.CommitSnapshot(a) != nil, "publication without intent accepted")
			Ok(t, s.BeginSnapshot(a))
			rows, err := s.Snapshot(j.ID)
			Ok(t, err)
			Assert(t, rows == nil, "unpublished snapshot was visible")
			Ok(t, s.MarkUncertain(p))
			Assert(t, s.CommitSnapshot(a) != nil, "uncertain owner published snapshot")
			rows, err = s.Snapshot(j.ID)
			Ok(t, err)
			Assert(t, rows == nil, "unpublished snapshot was visible")
		})
		t.Run("snapshot metadata is validated before publication", func(t *testing.T) {
			s := conformanceStore(t)
			p := claimPull()
			Ok(t, s.AcquireClaim(p, "owner"))
			p.Execution = &models.Execution{Owner: "owner"}
			j := claimJob(p, uuid.NewString())
			Ok(t, s.StartJob(j))
			snapshot := validSnapshot(j.ID)
			invalid := snapshot
			invalid.Manifest = models.SnapshotManifest{Version: 1, Files: []models.SnapshotFile{{Kind: "plan", Path: "../plan.tfplan", SHA256: snapshot.SHA256, Mode: 0600}}}
			Assert(t, s.BeginSnapshot(invalid) != nil, "unsafe snapshot manifest was reserved")
			Ok(t, s.BeginSnapshot(snapshot))
			Assert(t, s.BeginSnapshot(snapshot) != nil, "producer reserved a second bundle")
			for _, hash := range []string{"", "abc", strings.Repeat("z", 64)} {
				invalid = snapshot
				invalid.SHA256 = hash
				Assert(t, s.CommitSnapshot(invalid) != nil, "invalid bundle checksum was published")
			}
			invalid = snapshot
			invalid.Size = 0
			Assert(t, s.CommitSnapshot(invalid) != nil, "empty bundle was published")
			unpublished, err := s.Snapshot(j.ID)
			Ok(t, err)
			Assert(t, unpublished == nil, "invalid bundle became visible")
			Ok(t, s.CommitSnapshot(snapshot))
		})
		t.Run("cancellation invalidates mutating apply input", func(t *testing.T) {
			s := conformanceStore(t)
			p := claimPull()
			Ok(t, s.AcquireClaim(p, "owner"))
			p.Execution = &models.Execution{Owner: "owner"}
			producer := claimJob(p, uuid.NewString())
			Ok(t, s.StartJob(producer))
			Ok(t, s.StartPlan(producer.ID))
			a := validSnapshot(producer.ID)
			Ok(t, s.BeginSnapshot(a))
			Ok(t, s.CommitSnapshot(a))
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
			a := validSnapshot(j.ID)
			Ok(t, s.BeginSnapshot(a))
			Ok(t, s.CommitSnapshot(a))
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
