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
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

func TestDurableArtifactBackendWiring(t *testing.T) {
	for _, mode := range []string{"", "database", "filesystem"} {
		t.Run(mode, func(t *testing.T) {
			store, err := sqlstore.OpenServer(t.TempDir())
			Ok(t, err)
			defer store.Close()
			dataDir := t.TempDir()
			runtime, err := newStorageRuntime(store, UserConfig{DataDir: dataDir, Storage: &StorageConfig{Database: StorageDatabaseConfig{Type: "sqlite"}, Artifacts: StorageArtifactsConfig{Type: mode}}})
			Ok(t, err)
			defer runtime.Artifacts.Blobs.Close()
			_, database := runtime.Artifacts.Blobs.(sqlstore.BlobStore)
			_, filesystem := runtime.Artifacts.Blobs.(artifacts.ObjectSnapshots)
			Equals(t, mode != "filesystem", database)
			Equals(t, mode == "filesystem", filesystem)
			_, err = os.Stat(filepath.Join(dataDir, "artifacts"))
			Equals(t, mode == "filesystem", err == nil)
		})
	}
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
