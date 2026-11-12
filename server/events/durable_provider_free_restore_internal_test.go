// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/go-version"
	"github.com/runatlantis/atlantis/server/core/artifacts"
	"github.com/runatlantis/atlantis/server/core/config/valid"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/jobs"
	. "github.com/runatlantis/atlantis/testing"
)

func TestDurableRestoreWithoutExternalProviders(t *testing.T) {
	store, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	defer store.Close()
	blobs, err := artifacts.NewFilesystem(t.TempDir())
	Ok(t, err)
	defer blobs.Close()
	service := &jobs.Service{Store: store, Artifacts: &artifacts.Service{Records: store, Blobs: artifacts.ObjectSnapshots{Store: blobs}}}
	defer service.Close()
	tool := "terraform"
	repo := models.Repo{FullName: "org/repo"}
	ctx := command.ProjectContext{ArtifactRoot: t.TempDir(), JobID: uuid.NewString(), CommandName: command.Plan, BaseRepo: repo, Pull: models.PullRequest{BaseRepo: repo, Num: 1, HeadCommit: "revision", Execution: &models.Execution{Owner: "owner"}}, RepoRelDir: ".", Workspace: "default", RequiresAtlantisManagedPlanFile: true, TerraformVersion: version.Must(version.NewVersion("1.15.0")), TerraformDistribution: &tool, Steps: []valid.Step{{StepName: "init"}, {StepName: "plan"}}}
	Ok(t, store.AcquireClaim(ctx.Pull, "owner"))
	Ok(t, service.Begin(ctx))
	Ok(t, store.StartPlan(ctx.JobID))
	planPath := filepath.Join(ctx.ArtifactRoot, "default.tfplan")
	Ok(t, os.WriteFile(planPath, []byte("saved plan"), 0600))
	Ok(t, os.Mkdir(filepath.Join(ctx.ArtifactRoot, ".terraform"), 0700))
	Ok(t, os.WriteFile(filepath.Join(ctx.ArtifactRoot, ".terraform/terraform.tfstate"), []byte(`{"backend":{"type":"local"}}`), 0600))
	Ok(t, service.Artifacts.SnapshotPlanWithEnvironment(ctx, ctx.ArtifactRoot, planPath, nil))
	Ok(t, service.Finish(ctx, command.ProjectCommandOutput{}))
	ctx.JobID, ctx.CommandName = uuid.NewString(), command.Apply
	ctx.Steps = []valid.Step{{StepName: "apply"}}
	Ok(t, service.Begin(ctx))
	runner := &DefaultProjectCommandRunner{Jobs: service, Artifacts: service.Artifacts}
	restored, _, automatic, err := runner.prepareDurableRestore(ctx)
	Ok(t, err)
	Assert(t, automatic && restored.RestoredDataDirectory == ".terraform" && restored.RestoredLockHash == "", "provider-free restore did not retain backend initialization")
}
