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
	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/jobs"
	. "github.com/runatlantis/atlantis/testing"
)

func TestDurableRestoreAdmission(t *testing.T) {
	for _, name := range []string{"default", "explicit init", "changed version", "changed distribution", "missing version", "corrupt manifest", "custom setup", "custom init arguments", "old prerelease"} {
		t.Run(name, func(t *testing.T) {
			store, err := sqlstore.OpenServer(t.TempDir())
			Ok(t, err)
			defer store.Close()
			blobs, err := artifacts.NewFilesystem(t.TempDir())
			Ok(t, err)
			defer blobs.Close()
			service := &jobs.Service{Store: store, Artifacts: &artifacts.Service{Records: store, Blobs: artifacts.ObjectSnapshots{Store: blobs}}}
			defer service.Close()
			tool := "terraform"
			toolVersion := version.Must(version.NewVersion("1.15.0"))
			if name == "old prerelease" {
				toolVersion = version.Must(version.NewVersion("0.13.0-rc1"))
			}
			repo := models.Repo{FullName: "org/repo"}
			ctx := command.ProjectContext{ArtifactRoot: t.TempDir(), JobID: uuid.NewString(), CommandName: command.Plan, BaseRepo: repo, Pull: models.PullRequest{BaseRepo: repo, Num: 1, HeadCommit: "revision", Execution: &models.Execution{Owner: "owner"}}, RepoRelDir: ".", Workspace: "default", RequiresAtlantisManagedPlanFile: true, TerraformVersion: toolVersion, TerraformDistribution: &tool, Steps: []valid.Step{{StepName: "init"}, {StepName: "plan"}}}
			if name == "custom setup" {
				ctx.Steps = append([]valid.Step{{StepName: "run", RunCommand: "custom setup"}}, ctx.Steps...)
			}
			if name == "custom init arguments" {
				ctx.Steps[0].ExtraArgs = []string{"-backend-config=secret=value"}
			}
			if name == "missing version" {
				ctx.TerraformVersion = nil
			}
			Ok(t, store.AcquireClaim(ctx.Pull, "owner"))
			Ok(t, service.Begin(ctx))
			Ok(t, store.StartPlan(ctx.JobID))
			planPath := filepath.Join(ctx.ArtifactRoot, "default.tfplan")
			Ok(t, os.WriteFile(planPath, []byte("saved plan"), 0600))
			Ok(t, os.WriteFile(filepath.Join(ctx.ArtifactRoot, ".terraform.lock.hcl"), []byte("provider selections"), 0600))
			Ok(t, service.Artifacts.SnapshotPlanWithEnvironment(ctx, ctx.ArtifactRoot, planPath, nil))
			Ok(t, service.Finish(ctx, command.ProjectCommandOutput{}))
			if name == "corrupt manifest" {
				service.Artifacts.Records = corruptSnapshotStore{store}
			}
			consumer := ctx
			consumer.JobID, consumer.CommandName = uuid.NewString(), command.Apply
			consumer.Steps = []valid.Step{{StepName: "apply"}}
			if name == "explicit init" {
				consumer.Steps = []valid.Step{{StepName: "init"}, {StepName: "apply"}}
			}
			if name == "changed version" {
				consumer.TerraformVersion = version.Must(version.NewVersion("1.16.0"))
			}
			if name == "changed distribution" {
				alternate := "opentofu"
				consumer.TerraformDistribution = &alternate
			}
			Ok(t, service.Begin(consumer))
			runner := &DefaultProjectCommandRunner{Jobs: service, Artifacts: service.Artifacts}
			restored, _, automatic, err := runner.prepareDurableRestore(consumer)
			switch name {
			case "default":
				Ok(t, err)
				Assert(t, automatic && restored.RestoredDataDirectory == ".terraform" && restored.RestoredLockHash != "", "default restore was not bound to immutable initialization inputs")
			case "explicit init":
				Ok(t, err)
				Assert(t, !automatic && restored.RestoredDataDirectory != "", "explicit init did not retain restoration guards")
			case "old prerelease":
				Ok(t, err)
				Assert(t, !automatic && restored.RestoredDataDirectory == "", "old Terraform entered automatic lock-file initialization")
			default:
				Assert(t, err != nil, "unsafe restore was admitted")
			}
		})
	}
}

type corruptSnapshotStore struct{ db.SnapshotStore }

func (s corruptSnapshotStore) InputSnapshot(id string) (*models.PlanSnapshot, error) {
	snapshot, err := s.SnapshotStore.InputSnapshot(id)
	if snapshot != nil {
		snapshot.Manifest.DataDirectory = "../escape"
	}
	return snapshot, err
}
