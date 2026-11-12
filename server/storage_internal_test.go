// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/logs"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

func TestArtifactCollectionConfinedToReservedNamespace(t *testing.T) {
	store, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	defer store.Close()
	directory := t.TempDir()
	runtime, err := newStorageRuntime(store, UserConfig{Storage: &StorageConfig{Database: StorageDatabaseConfig{Type: "sqlite"}, Artifacts: StorageArtifactsConfig{Type: "filesystem", Directory: directory}}})
	Ok(t, err)
	defer runtime.Artifacts.Blobs.Close()
	unrelated := filepath.Join(directory, "shared.plan")
	orphan := filepath.Join(directory, "snapshots", "v2", uuid.NewString(), "plan-snapshot.tar.gz")
	Ok(t, os.MkdirAll(filepath.Dir(orphan), 0700))
	old := time.Now().Add(-2 * time.Hour)
	for _, file := range []string{unrelated, orphan} {
		Ok(t, os.WriteFile(file, []byte("data"), 0600))
		Ok(t, os.Chtimes(file, old, old))
	}
	Ok(t, runtime.Artifacts.Collect(time.Now(), time.Hour, 0))
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
	for _, mode := range []string{"", "database", "filesystem"} {
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
			Assert(t, runtime.Jobs.Logs == runtime.Logs, "job writers and maintenance use different log services")
			job := runtimeLogJob(t, runtime)
			data, err := runtime.Logs.ReadAll(job.ID)
			Ok(t, err)
			Equals(t, "persisted\n", string(data))
			_, chunks, err := store.ReadLogJournal(job.ID, 0, 1024)
			Ok(t, err)
			if mode == "filesystem" {
				Equals(t, 0, len(chunks))
				data, err = os.ReadFile(filepath.Join(directory, filepath.FromSlash(logs.Key(job.ID))))
				Ok(t, err)
				Equals(t, "persisted\n", string(data))
			} else {
				Equals(t, "database", runtime.Logs.Mode)
				Equals(t, "persisted\n", string(chunks))
			}
		})
	}
}

func TestDurableLogMaintenanceArchivesOutsideArtifactNamespace(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_SESSION_TOKEN", "")
	var objectKey string
	var object []byte
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut:
			objectKey = strings.TrimPrefix(r.URL.Path, "/bucket/")
			var err error
			object, err = io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusInternalServerError)
			}
		case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></ListBucketResult>`)
		case r.Method == http.MethodGet:
			data := object
			if value := r.Header.Get("Range"); value != "" {
				var start, end int
				if _, err := fmt.Sscanf(value, "bytes=%d-%d", &start, &end); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				data = object[start : end+1]
			}
			_, _ = w.Write(data)
		default:
			t.Errorf("unexpected object request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer endpoint.Close()
	store, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	defer store.Close()
	runtime, err := newStorageRuntime(store, UserConfig{Storage: &StorageConfig{Database: StorageDatabaseConfig{Type: "sqlite"}, Artifacts: StorageArtifactsConfig{Type: "s3", S3: StorageS3Config{Bucket: "bucket", Region: "us-east-1", Prefix: "shared", Endpoint: endpoint.URL, ForcePathStyle: true}}}})
	Ok(t, err)
	defer runtime.Artifacts.Blobs.Close()
	job := runtimeLogJob(t, runtime)
	runtime.Maintain(&events.PullClosedExecutor{}, logging.NewNoopLogger(t))()
	info, remaining, err := store.ReadLogJournal(job.ID, 0, 1024)
	Ok(t, err)
	Assert(t, info.ArchivedAt != nil, "maintenance did not publish the completed archive")
	Equals(t, 0, len(remaining))
	Equals(t, "shared/"+logs.Key(job.ID), objectKey)
	Equals(t, "persisted\n", string(object))
	data, err := runtime.Logs.ReadAll(job.ID)
	Ok(t, err)
	Equals(t, "persisted\n", string(data))
}
