// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/artifacts"
	"github.com/runatlantis/atlantis/server/core/artifacts/artifactstest"
	"github.com/runatlantis/atlantis/server/core/logs"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

// fakeS3 replaces the S3 client constructor for one test.
func fakeS3(t *testing.T, bucket string) *artifactstest.S3 {
	t.Helper()
	fake := artifactstest.New(bucket)
	previous := newS3Client
	newS3Client = func(c artifacts.S3Config) (artifacts.S3Client, error) {
		if c.Bucket != bucket {
			return nil, errors.New("unexpected bucket")
		}
		return fake, nil
	}
	t.Cleanup(func() { newS3Client = previous })
	return fake
}

func s3Artifacts(prefix string) StorageArtifactsConfig {
	return StorageArtifactsConfig{Type: "s3", S3: StorageS3Config{Bucket: "artifacts", Region: "us-east-1", Prefix: prefix}}
}

func TestDurableArtifactBackendWiring(t *testing.T) {
	for _, mode := range []string{"", "database", "filesystem", "s3"} {
		t.Run(mode, func(t *testing.T) {
			store, err := sqlstore.OpenServer(t.TempDir())
			Ok(t, err)
			defer store.Close()
			dataDir := t.TempDir()
			config := StorageArtifactsConfig{Type: mode}
			if mode == "s3" {
				fakeS3(t, "artifacts")
				config = s3Artifacts("atlantis")
			}
			runtime, err := newStorageRuntime(store, UserConfig{DataDir: dataDir, Storage: &StorageConfig{Database: StorageDatabaseConfig{Type: "sqlite"}, Artifacts: config}})
			Ok(t, err)
			defer runtime.Artifacts.Blobs.Close()
			_, database := runtime.Artifacts.Blobs.(sqlstore.BlobStore)
			objects, object := runtime.Artifacts.Blobs.(artifacts.ObjectSnapshots)
			Equals(t, mode == "" || mode == "database", database)
			Equals(t, mode == "filesystem" || mode == "s3", object)
			_, err = os.Stat(filepath.Join(dataDir, "artifacts"))
			Equals(t, mode == "filesystem", err == nil)
			if mode == "s3" {
				_, isS3 := objects.Store.(*artifacts.S3)
				Assert(t, isS3, "s3 type selected another object store")
				Assert(t, runtime.Logs.Archive == objects.Store, "logs and plans use different object stores")
			}
		})
	}
}

func TestDurableS3StartupFailureStopsStartup(t *testing.T) {
	store, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	defer store.Close()
	previous := newS3Client
	newS3Client = func(artifacts.S3Config) (artifacts.S3Client, error) {
		return nil, errors.New(`validating S3 bucket "artifacts" forbidden`)
	}
	t.Cleanup(func() { newS3Client = previous })
	_, err = newStorageRuntime(store, UserConfig{DataDir: t.TempDir(), Storage: &StorageConfig{Database: StorageDatabaseConfig{Type: "sqlite"}, Artifacts: s3Artifacts("")}})
	ErrContains(t, "opening artifact storage", err)
}

func TestArtifactCollectionConfinedToReservedNamespace(t *testing.T) {
	store, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	defer store.Close()
	directory := t.TempDir()
	runtime, err := newStorageRuntime(store, UserConfig{Storage: &StorageConfig{Database: StorageDatabaseConfig{Type: "sqlite"}, Artifacts: StorageArtifactsConfig{Type: "filesystem", Directory: directory}}})
	Ok(t, err)
	defer runtime.Artifacts.Blobs.Close()
	unrelated := filepath.Join(directory, "shared.plan")
	orphan := filepath.Join(directory, "snapshots", "v1", uuid.NewString(), "plan-snapshot.tar.gz")
	Ok(t, os.MkdirAll(filepath.Dir(orphan), 0700))
	old := time.Now().Add(-2 * time.Hour)
	for _, file := range []string{unrelated, orphan} {
		Ok(t, os.WriteFile(file, []byte("data"), 0600))
		Ok(t, os.Chtimes(file, old, old))
	}
	Ok(t, runtime.Artifacts.Collect(time.Now(), 0))
	_, err = os.Stat(unrelated)
	Ok(t, err)
	_, err = os.Stat(orphan)
	Assert(t, os.IsNotExist(err), "orphan was not removed")
}

type lifecycleReader struct {
	current models.PullRequest
	err     error
}

func (r *lifecycleReader) GetLivePullIdentity(command.ProjectContext) (models.PullRequest, error) {
	return r.current, r.err
}

func TestDurableMaintenanceRetriesLifecycleVerification(t *testing.T) {
	store, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	defer store.Close()
	runtime, err := newStorageRuntime(store, UserConfig{Storage: &StorageConfig{Database: StorageDatabaseConfig{Type: "sqlite"}}})
	Ok(t, err)
	defer runtime.Artifacts.Blobs.Close()
	pull := models.PullRequest{BaseRepo: models.Repo{FullName: "org/repo"}, Num: 1, State: models.OpenPullState}
	Ok(t, store.QueueLifecycleCheck(pull))
	reader := &lifecycleReader{current: pull, err: errors.New("VCS unavailable")}
	worker := &events.PullClosedExecutor{LivePullHeadFetcher: reader}
	maintain := runtime.Maintain(worker, logging.NewNoopLogger(t))
	maintain()
	ErrContains(t, "awaiting VCS verification", store.AcquireClaim(pull, "blocked"))
	reader.err = nil
	maintain()
	pending, err := store.PendingLifecycleChecks()
	Ok(t, err)
	Equals(t, 0, len(pending))
	Ok(t, store.AcquireClaim(pull, "verified"))
}

func runtimeLogJob(t *testing.T, runtime *storageRuntime) models.Job {
	t.Helper()
	pull := models.PullRequest{Num: -1, BaseRepo: models.Repo{FullName: "org/repo"}, Execution: &models.Execution{Owner: uuid.NewString()}}
	Ok(t, runtime.Store.AcquireClaim(pull, pull.ExecutionOwner()))
	job := models.Job{ID: uuid.NewString(), Kind: models.CommandJob, Command: "version", Pull: pull, Project: models.NewProject(pull.BaseRepo.FullName, ".", ""), Workspace: "default"}
	Ok(t, runtime.Jobs.Start(job))
	Ok(t, runtime.Jobs.AppendLog(job.ID, "persisted"))
	Ok(t, runtime.Jobs.Complete(job.ID, ""))
	Ok(t, runtime.Store.ReleaseClaim(pull))
	return job
}

func TestDurableLogBackendWiring(t *testing.T) {
	for _, mode := range []string{"", "database", "filesystem", "s3"} {
		t.Run(mode, func(t *testing.T) {
			store, err := sqlstore.OpenServer(t.TempDir())
			Ok(t, err)
			defer store.Close()
			directory := t.TempDir()
			artifactConfig := StorageArtifactsConfig{Type: mode}
			if mode == "filesystem" {
				artifactConfig.Directory = directory
			}
			if mode == "s3" {
				fakeS3(t, "artifacts")
				artifactConfig = s3Artifacts("")
			}
			runtime, err := newStorageRuntime(store, UserConfig{DataDir: t.TempDir(), Storage: &StorageConfig{Database: StorageDatabaseConfig{Type: "sqlite"}, Artifacts: artifactConfig}})
			Ok(t, err)
			defer runtime.Artifacts.Blobs.Close()
			Assert(t, runtime.Jobs.Logs == runtime.Logs, "job writers and maintenance use different log services")
			job := runtimeLogJob(t, runtime)
			data, err := runtime.Logs.ReadAll(job.ID)
			Ok(t, err)
			Equals(t, "persisted\n", string(data))
			// Sealed output stays in the journal until maintenance runs, for every backend.
			_, chunks, err := store.ReadLogJournal(job.ID, 0, 1024)
			Ok(t, err)
			Equals(t, "persisted\n", string(chunks))
			Equals(t, mode == "filesystem" || mode == "s3", runtime.Logs.Archive != nil)
		})
	}
}

func TestDurableLogMaintenanceArchivesOutsideArtifactNamespace(t *testing.T) {
	for _, mode := range []string{"database", "filesystem"} {
		t.Run(mode, func(t *testing.T) {
			store, err := sqlstore.OpenServer(t.TempDir())
			Ok(t, err)
			defer store.Close()
			directory := t.TempDir()
			artifactConfig := StorageArtifactsConfig{Type: mode}
			if mode == "filesystem" {
				artifactConfig.Directory = directory
			}
			runtime, err := newStorageRuntime(store, UserConfig{Storage: &StorageConfig{Database: StorageDatabaseConfig{Type: "sqlite"}, Artifacts: artifactConfig}})
			Ok(t, err)
			defer runtime.Artifacts.Blobs.Close()
			job := runtimeLogJob(t, runtime)
			runtime.Maintain(&events.PullClosedExecutor{}, logging.NewNoopLogger(t))()
			info, remaining, err := store.ReadLogJournal(job.ID, 0, 1024)
			Ok(t, err)
			archivePath := filepath.Join(directory, filepath.FromSlash(logs.Key(job.ID)))
			if mode == "filesystem" {
				Assert(t, info.ArchivedAt != nil, "maintenance did not publish the completed archive")
				Equals(t, 0, len(remaining))
				object, err := os.ReadFile(archivePath)
				Ok(t, err)
				Equals(t, "persisted\n", string(object))
				// Artifact orphan collection must leave the log namespace alone.
				Ok(t, runtime.Artifacts.Collect(time.Now().Add(24*time.Hour), 0))
				_, err = os.Stat(archivePath)
				Ok(t, err)
			} else {
				Assert(t, info.ArchivedAt == nil, "database backend archived its final journal")
				Equals(t, "persisted\n", string(remaining))
				_, err = os.Stat(archivePath)
				Assert(t, os.IsNotExist(err), "database backend wrote a log file")
			}
			data, err := runtime.Logs.ReadAll(job.ID)
			Ok(t, err)
			Equals(t, "persisted\n", string(data))
		})
	}
}

func TestDurableHeartbeatRefreshesAndSweeps(t *testing.T) {
	store, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	defer store.Close()
	runtime, err := newStorageRuntime(store, UserConfig{Storage: &StorageConfig{Database: StorageDatabaseConfig{Type: "sqlite"}}})
	Ok(t, err)
	defer runtime.Artifacts.Blobs.Close()
	pull := models.PullRequest{BaseRepo: models.Repo{FullName: "org/repo"}, Num: 1, State: models.OpenPullState}
	pull.Execution = &models.Execution{Owner: uuid.NewString()}
	Ok(t, store.AcquireClaim(pull, pull.ExecutionOwner()))
	heartbeat := runtime.Heartbeat(logging.NewNoopLogger(t))
	heartbeat()
	// A refreshed claim is live, so the sweep in the same round leaves it active.
	Ok(t, store.CheckClaim(pull))
}

func TestSharedArtifactsWarning(t *testing.T) {
	dataDir := t.TempDir()
	shared := t.TempDir()
	for _, c := range []struct {
		name      string
		database  string
		artifacts StorageArtifactsConfig
		warn      bool
	}{
		{"sqlite filesystem", "sqlite", StorageArtifactsConfig{Type: "filesystem"}, false},
		{"postgres database", "postgres", StorageArtifactsConfig{Type: "database"}, false},
		{"postgres default directory", "postgres", StorageArtifactsConfig{Type: "filesystem"}, true},
		{"postgres directory inside data-dir", "postgres", StorageArtifactsConfig{Type: "filesystem", Directory: filepath.Join(dataDir, "shared")}, true},
		{"postgres shared directory", "postgres", StorageArtifactsConfig{Type: "filesystem", Directory: shared}, false},
		{"postgres sibling directory", "postgres", StorageArtifactsConfig{Type: "filesystem", Directory: dataDir + "-artifacts"}, false},
		{"postgres s3", "postgres", StorageArtifactsConfig{Type: "s3", S3: StorageS3Config{Bucket: "artifacts", Region: "us-east-1"}}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			config := UserConfig{DataDir: dataDir, Storage: &StorageConfig{Database: StorageDatabaseConfig{Type: c.database}, Artifacts: c.artifacts}}
			Equals(t, c.warn, sharedArtifactsWarning(config) != "")
		})
	}
}

// Maintenance archives sealed logs to S3 under the configured prefix, and
// artifact orphan collection leaves the log namespace alone.
func TestDurableS3MaintenanceArchivesLogsBesidePlans(t *testing.T) {
	store, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	defer store.Close()
	fake := fakeS3(t, "artifacts")
	runtime, err := newStorageRuntime(store, UserConfig{DataDir: t.TempDir(), Storage: &StorageConfig{Database: StorageDatabaseConfig{Type: "sqlite"}, Artifacts: s3Artifacts("team/atlantis")}})
	Ok(t, err)
	defer runtime.Artifacts.Blobs.Close()
	job := runtimeLogJob(t, runtime)
	runtime.Maintain(&events.PullClosedExecutor{}, logging.NewNoopLogger(t))()
	info, remaining, err := store.ReadLogJournal(job.ID, 0, 1024)
	Ok(t, err)
	Assert(t, info.ArchivedAt != nil, "maintenance did not publish the completed archive")
	Equals(t, 0, len(remaining))
	object, ok := fake.Object("team/atlantis/" + logs.Key(job.ID))
	Assert(t, ok, "archive object missing under the prefix")
	Equals(t, "persisted\n", string(object))
	Ok(t, runtime.Artifacts.Collect(time.Now().Add(24*time.Hour), 0))
	_, ok = fake.Object("team/atlantis/" + logs.Key(job.ID))
	Assert(t, ok, "artifact collection removed a log archive")
	data, err := runtime.Logs.ReadAll(job.ID)
	Ok(t, err)
	Equals(t, "persisted\n", string(data))
}
