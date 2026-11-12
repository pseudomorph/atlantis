// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/artifacts"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/jobs"
	. "github.com/runatlantis/atlantis/testing"
)

// Queue verification immediately after a successful preliminary cancellation
// check, before the job service starts its admission transaction.
type lifecycleAfterPrecheck struct {
	*sqlstore.Store
	checks int
}

func (s *lifecycleAfterPrecheck) IsCancelled(p models.PullRequest) (bool, error) {
	stopped, err := s.Store.IsCancelled(p)
	if err != nil || stopped {
		return stopped, err
	}
	s.checks++
	if s.checks == 1 {
		if err := s.QueueLifecycleCheck(p); err != nil {
			return false, err
		}
	}
	return false, nil
}

func TestDurableJobAdmissionRechecksPendingClosure(t *testing.T) {
	store, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	defer store.Close()
	blobs, err := artifacts.NewFilesystem(t.TempDir())
	Ok(t, err)
	defer blobs.Close()
	service := &jobs.Service{Store: store, Artifacts: &artifacts.Service{Records: store, Blobs: artifacts.ObjectSnapshots{Store: blobs}}}
	defer service.Close()
	repo := models.Repo{FullName: "org/repo"}
	ctx := command.ProjectContext{ArtifactRoot: t.TempDir(), JobID: uuid.NewString(), BaseRepo: repo, Pull: models.PullRequest{Num: 1, BaseRepo: repo, HeadCommit: "revision", Execution: &models.Execution{Owner: "owner"}}, RepoRelDir: ".", Workspace: "default", CommandName: command.Plan, RequiresAtlantisManagedPlanFile: true}
	Ok(t, store.AcquireClaim(ctx.Pull, "owner"))
	Ok(t, service.Begin(ctx))
	Ok(t, store.StartPlan(ctx.JobID))
	path := filepath.Join(ctx.ArtifactRoot, "default.tfplan")
	Ok(t, os.WriteFile(path, []byte("saved plan"), 0600))
	Ok(t, service.Artifacts.SnapshotPlan(ctx, ctx.ArtifactRoot, path))
	Ok(t, service.Finish(ctx, command.ProjectCommandOutput{PlanSuccess: &models.PlanSuccess{}}))
	planID := ctx.JobID
	ctx.JobID = uuid.NewString()
	checks := &lifecycleAfterPrecheck{Store: store}
	runner := &DefaultProjectCommandRunner{Jobs: service, Artifacts: service.Artifacts, Execution: &ExecutionCoordinator{Store: checks}}
	executed := false
	result := runner.withDurableJob(ctx, command.Apply, func(command.ProjectContext) command.ProjectCommandOutput {
		executed = true
		return command.ProjectCommandOutput{}
	})
	ErrContains(t, "starting durable job", result.Error)
	ErrContains(t, "awaiting VCS verification", result.Error)
	Equals(t, 1, checks.checks)
	Assert(t, !executed, "queued project executed after closure verification was queued")
	recorded, err := store.Job(ctx.JobID)
	Ok(t, err)
	Assert(t, recorded == nil, "rejected apply was recorded as running")
	pending, err := store.PendingPlans(repo, ctx.Pull.Num)
	Ok(t, err)
	Equals(t, 1, len(pending))
	Equals(t, planID, pending[0].ID)
	Ok(t, store.CheckClaim(ctx.Pull))
	Ok(t, store.ReleaseClaim(ctx.Pull))
}
