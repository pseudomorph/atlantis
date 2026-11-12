// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"testing"

	"github.com/runatlantis/atlantis/server/core/locking"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

type noopWorkingDir struct{ WorkingDir }

func (noopWorkingDir) Delete(logging.SimpleLogging, models.Repo, models.PullRequest) error {
	return nil
}

func TestReopenedPullAcceptsWorkAfterDeferredCleanup(t *testing.T) {
	store, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	defer store.Close()
	logger := logging.NewNoopLogger(t)
	repo := models.Repo{FullName: "org/repo", Owner: "org", Name: "repo", VCSHost: models.VCSHost{Hostname: "git.example", Type: models.Gitlab}}
	pull := models.PullRequest{BaseRepo: repo, Num: 3, HeadCommit: "abc", State: models.OpenPullState}
	cleaner := &PullClosedExecutor{Executions: store, Database: store, Locker: locking.NewClient(store), WorkingDir: noopWorkingDir{}, PullClosedTemplate: &PullClosedEventTemplate{}}

	// Work still running when the pull request closes defers its cleanup.
	busy := pull
	busy.Execution = &models.Execution{Owner: "closing-work"}
	Ok(t, store.AcquireClaim(busy, "closing-work"))
	ErrContains(t, "cleanup deferred", cleaner.CleanUpPull(logger, repo, pull))

	Ok(t, store.RequestReopen(pull))
	ErrContains(t, "has not finished", store.AcquireClaim(pull, "too-early"))
	Ok(t, store.ReleaseClaim(busy))

	// Maintenance retries the cleanup; the reopen request must survive it.
	ready, err := store.ClosedPullsReady()
	Ok(t, err)
	Equals(t, 1, len(ready))
	Ok(t, cleaner.CleanUpPull(logger, repo, ready[0]))
	Ok(t, store.AcquireClaim(pull, "after-reopen"))
}
