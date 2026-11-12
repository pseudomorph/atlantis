// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/artifacts"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestObjectSnapshotsIsolatesJobsAndLogArchives(t *testing.T) {
	factories := map[string]func(*testing.T) artifacts.Store{
		"filesystem": func(t *testing.T) artifacts.Store {
			store, err := artifacts.NewFilesystem(t.TempDir())
			Ok(t, err)
			t.Cleanup(func() { _ = store.Close() })
			return store
		},
		"s3 API": func(t *testing.T) artifacts.Store {
			return artifacts.NewS3WithClient(&objectClient{objects: map[string][]byte{}}, "bucket", "prefix")
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			raw := factory(t)
			backend := artifacts.ObjectSnapshots{Store: raw}
			first, second := uuid.NewString(), uuid.NewString()
			_, err := backend.PutSnapshot(first, strings.NewReader("first bundle"))
			Ok(t, err)
			_, err = backend.PutSnapshot(second, strings.NewReader("second bundle"))
			Ok(t, err)
			_, err = raw.Put("logs/v1/"+first, strings.NewReader("log archive"))
			Ok(t, err)
			reader, err := backend.OpenSnapshot(first)
			Ok(t, err)
			data, err := io.ReadAll(reader)
			Ok(t, err)
			Ok(t, reader.Close())
			Equals(t, []byte("first bundle"), data)
			_, err = backend.PutSnapshot("../outside", bytes.NewReader(nil))
			Assert(t, err != nil, "accepted unsafe identity")
			Ok(t, backend.CollectOrphans(map[string]bool{first: true}, false, time.Now().Add(2*time.Hour)))
			reader, err = backend.OpenSnapshot(second)
			Ok(t, err)
			Ok(t, reader.Close())
			Ok(t, backend.CollectOrphans(map[string]bool{first: true}, true, time.Now().Add(2*time.Hour)))
			_, err = backend.OpenSnapshot(second)
			Assert(t, err != nil, "orphan snapshot remained")
			reader, err = raw.Open("logs/v1/" + first)
			Ok(t, err)
			Ok(t, reader.Close())
			reader, err = backend.OpenSnapshot(first)
			Ok(t, err)
			Ok(t, reader.Close())
			Ok(t, backend.DeleteSnapshot(first))
			Ok(t, backend.DeleteSnapshot(first))
		})
	}
}

func TestSnapshotBackendWorkflowConformance(t *testing.T) {
	for _, mode := range []string{"database", "filesystem", "s3"} {
		t.Run(mode, func(t *testing.T) {
			store, err := sqlstore.OpenServer(t.TempDir())
			Ok(t, err)
			defer store.Close()
			var backend artifacts.SnapshotBackend
			switch mode {
			case "database":
				backend = sqlstore.BlobStore{Store: store}
			case "filesystem":
				raw, err := artifacts.NewFilesystem(t.TempDir())
				Ok(t, err)
				backend = artifacts.ObjectSnapshots{Store: raw}
			case "s3":
				backend = artifacts.ObjectSnapshots{Store: artifacts.NewS3WithClient(&objectClient{objects: map[string][]byte{}}, "bucket", "prefix")}
			}
			defer backend.Close()
			files := &artifacts.Service{Records: store, Blobs: backend, WorkflowFiles: []string{".artifacts"}}
			repo := models.Repo{FullName: "team/repo"}
			pull := models.PullRequest{BaseRepo: repo, Num: 1, HeadCommit: "revision", Execution: &models.Execution{Owner: "owner"}}
			Ok(t, store.AcquireClaim(pull, "owner"))
			producer := models.Job{ID: uuid.NewString(), Pull: pull, Project: models.NewProject(repo.FullName, ".", ""), Workspace: "default", Command: "plan", ManagedPlan: true, Revision: pull.HeadCommit}
			Ok(t, store.StartJob(producer))
			Ok(t, store.StartPlan(producer.ID))
			source := t.TempDir()
			plan := filepath.Join(source, "default.tfplan")
			Ok(t, os.WriteFile(plan, []byte{0, 1, 255}, 0600))
			Ok(t, os.WriteFile(filepath.Join(source, ".terraform.lock.hcl"), []byte("locked providers"), 0600))
			Ok(t, os.MkdirAll(filepath.Join(source, ".artifacts", "nested", "empty"), 0700))
			Ok(t, os.WriteFile(filepath.Join(source, ".artifacts", "nested", "custom.bin"), []byte{255, 0, 42}, 0600))
			ctx := command.ProjectContext{JobID: producer.ID, BaseRepo: repo, Pull: pull, Workspace: "default", RepoRelDir: ".", ArtifactRoot: source, RequiresAtlantisManagedPlanFile: true}
			Ok(t, files.SnapshotPlan(ctx, source, plan))
			Assert(t, files.SnapshotPlan(ctx, source, plan) != nil, "published a second snapshot for one job")
			Ok(t, store.FinishJob(producer.ID, "succeeded", ""))
			consumer := producer
			consumer.ID = uuid.NewString()
			consumer.Command = "apply"
			Ok(t, store.StartJob(consumer))
			snapshot, err := store.InputSnapshot(consumer.ID)
			Ok(t, err)
			Assert(t, snapshot != nil, "snapshot binding missing")
			destination := t.TempDir()
			ctx.JobID = consumer.ID
			ctx.ArtifactRoot = destination
			Ok(t, files.Load(ctx, filepath.Join(destination, "default.tfplan")))
			for name, want := range map[string][]byte{"default.tfplan": {0, 1, 255}, ".terraform.lock.hcl": []byte("locked providers"), ".artifacts/nested/custom.bin": {255, 0, 42}} {
				got, err := os.ReadFile(filepath.Join(destination, name))
				Ok(t, err)
				Equals(t, want, got)
			}
			info, err := os.Stat(filepath.Join(destination, ".artifacts", "nested", "empty"))
			Ok(t, err)
			Assert(t, info.IsDir(), "empty declared directory missing")
			Ok(t, store.FinishJob(consumer.ID, "succeeded", ""))
			Ok(t, store.ReleaseClaim(pull))
		})
	}
}
