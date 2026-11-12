// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"errors"
	"testing"

	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

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
	runtime, err := newStorageRuntime(store)
	Ok(t, err)
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
