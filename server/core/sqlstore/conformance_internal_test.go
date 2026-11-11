// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"testing"

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
func seedClaimJob(t *testing.T, s *Store, p models.PullRequest, state string) job {
	t.Helper()
	project := models.NewProject(p.BaseRepo.FullName, ".", "")
	acquired, _, err := s.TryLock(models.ProjectLock{Pull: p, Project: project, Workspace: "default"})
	Ok(t, err)
	Assert(t, acquired, "project lock unavailable")
	var pr pull
	Ok(t, s.db.Where("id IN (?)", pullIDs(s.db, p.BaseRepo, p.Num)).Take(&pr).Error)
	var projectID string
	Ok(t, projectIDs(s.db, p.BaseRepo, project, "default").Scan(&projectID).Error)
	started, err := s.Now()
	Ok(t, err)
	row := job{ID: uuid.NewString(), RepositoryID: pr.RepositoryID, PullID: pr.ID, ProjectID: projectID,
		Owner: p.ExecutionOwner(), Kind: models.CommandJob, ManagedPlan: true,
		State: state, StartedAt: started}
	Ok(t, s.db.Create(&row).Error)
	return row
}

func TestExecutionDatabaseConformance(t *testing.T) {
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
		seedClaimJob(t, s, p, "running")
		Assert(t, s.ReleaseClaim(p) != nil, "unfinished jobs released execution ownership")
		Ok(t, s.Close())
		s, err = OpenServer(dir)
		Ok(t, err)
		Ok(t, s.MarkPreviousExecutionsUncertain())
		Assert(t, s.CheckClaim(p) != nil, "uncertain owner remained active")
		Assert(t, s.AcquireClaim(p, "owner-b") != nil, "unfinished work allowed takeover")
		locked, err := s.GetLock(p.BaseRepo, models.NewProject(p.BaseRepo.FullName, ".", ""), "default")
		Ok(t, err)
		Assert(t, locked != nil, "startup removed user project lock")
	})
	t.Run("closure invalidates pending plans without releasing ownership", func(t *testing.T) {
		s := conformanceStore(t)
		p := claimPull()
		Ok(t, s.AcquireClaim(p, "owner"))
		p.Execution = &models.Execution{Owner: "owner"}
		row := seedClaimJob(t, s, p, "succeeded")
		Ok(t, s.db.Create(&pendingPlan{ProjectID: row.ProjectID, PullID: row.PullID, JobID: row.ID, Ready: true}).Error)
		Ok(t, s.ClosePull(p))
		var pending int64
		Ok(t, s.db.Model(&pendingPlan{}).Count(&pending).Error)
		Equals(t, int64(0), pending)
		idle, err := s.PullIdle(p)
		Ok(t, err)
		Assert(t, !idle, "closure discarded execution ownership")
		Assert(t, s.AcquireClaim(p, "contender") != nil, "closed pull accepted execution")
		Ok(t, s.ReleaseClaim(p))
		idle, err = s.PullIdle(p)
		Ok(t, err)
		Assert(t, idle, "stopped execution remained owned")
	})
	t.Run("cancellation preserves active ownership", func(t *testing.T) {
		s := conformanceStore(t)
		p := claimPull()
		Ok(t, s.AcquireClaim(p, "owner"))
		p.Execution = &models.Execution{Owner: "owner"}
		row := seedClaimJob(t, s, p, "running")
		Ok(t, s.CancelPull(p))
		cancelled, err := s.IsCancelled(p)
		Ok(t, err)
		Assert(t, cancelled, "cancellation was not recorded")
		Ok(t, s.CheckClaim(p))
		Assert(t, s.ReleaseClaim(p) != nil, "cancellation released unfinished work")
		Ok(t, s.db.Model(&job{}).Where("id = ?", row.ID).Update("state", "succeeded").Error)
		Ok(t, s.ReleaseClaim(p))
		Ok(t, s.AcquireClaim(p, "next-owner"))
		cancelled, err = s.IsCancelled(p)
		Ok(t, err)
		Assert(t, !cancelled, "new execution inherited prior cancellation")
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
}
