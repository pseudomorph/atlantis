// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/artifacts"
	"github.com/runatlantis/atlantis/server/core/artifacts/artifactstest"
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
		"s3": func(t *testing.T) artifacts.Store {
			fake := artifactstest.New("artifacts")
			store, err := artifacts.NewS3(fake, fake.Bucket, "atlantis", filepath.Join(t.TempDir(), "uploads"))
			Ok(t, err)
			return store
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
				fake := artifactstest.New("artifacts")
				raw, err := artifacts.NewS3(fake, fake.Bucket, "atlantis", filepath.Join(t.TempDir(), "uploads"))
				Ok(t, err)
				backend = artifacts.ObjectSnapshots{Store: raw}
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

// A crash between Filesystem.Put's temporary write and its rename leaves the
// temporary file behind. Collection removes stale ones and keeps writes that may
// still be in progress.
func TestObjectSnapshotsCollectsStaleWriteTemporaries(t *testing.T) {
	root := t.TempDir()
	raw, err := artifacts.NewFilesystem(root)
	Ok(t, err)
	defer raw.Close()
	backend := artifacts.ObjectSnapshots{Store: raw}
	id := uuid.NewString()
	_, err = backend.PutSnapshot(id, strings.NewReader("bundle"))
	Ok(t, err)
	now := time.Now()
	stale := "snapshots/v1/" + id + "/plan-snapshot.tar.gz." + uuid.NewString() + ".tmp"
	fresh := "snapshots/v1/" + id + "/plan-snapshot.tar.gz." + uuid.NewString() + ".tmp"
	unrelated := "snapshots/v1/" + id + "/notes.tmp"
	for _, key := range []string{stale, fresh, unrelated} {
		_, err = raw.Put(key, strings.NewReader("partial"))
		Ok(t, err)
	}
	old := now.Add(-2 * time.Hour)
	for _, key := range []string{stale, unrelated, "snapshots/v1/" + id + "/plan-snapshot.tar.gz"} {
		Ok(t, os.Chtimes(filepath.Join(root, filepath.FromSlash(key)), old, old))
	}

	Ok(t, backend.CollectOrphans(map[string]bool{id: true}, false, now))
	_, err = os.Stat(filepath.Join(root, filepath.FromSlash(stale)))
	Ok(t, err)

	Ok(t, backend.CollectOrphans(map[string]bool{id: true}, true, now))
	_, err = os.Stat(filepath.Join(root, filepath.FromSlash(stale)))
	Assert(t, os.IsNotExist(err), "stale write temporary remained")
	for _, key := range []string{fresh, unrelated} {
		_, err = os.Stat(filepath.Join(root, filepath.FromSlash(key)))
		Ok(t, err)
	}
	reader, err := backend.OpenSnapshot(id)
	Ok(t, err)
	Ok(t, reader.Close())
}

// S3 objects appear only after a complete PutObject, so collection removes
// unreferenced bundles by age and never touches keys outside the prefix.
func TestS3SnapshotOrphansConfinedToPrefix(t *testing.T) {
	fake := artifactstest.New("shared")
	raw, err := artifacts.NewS3(fake, fake.Bucket, "atlantis", filepath.Join(t.TempDir(), "uploads"))
	Ok(t, err)
	backend := artifacts.ObjectSnapshots{Store: raw}
	referenced, orphan, fresh := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now()
	old := now.Add(-2 * time.Hour)
	key := func(prefix, id string) string { return prefix + "snapshots/v1/" + id + "/plan-snapshot.tar.gz" }
	fake.Set(key("atlantis/", referenced), []byte("kept"), old)
	fake.Set(key("atlantis/", orphan), []byte("orphan"), old)
	fake.Set(key("atlantis/", fresh), []byte("uploading"), now)
	fake.Set(key("other/", orphan), []byte("foreign"), old)
	fake.Set(key("", orphan), []byte("foreign"), old)
	Ok(t, backend.CollectOrphans(map[string]bool{referenced: true}, true, now))
	want := []string{key("", orphan), key("atlantis/", fresh), key("atlantis/", referenced), key("other/", orphan)}
	slices.Sort(want)
	Equals(t, want, fake.Keys())
}
