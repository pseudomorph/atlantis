// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/runatlantis/atlantis/server/core/locking"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

type recoveryWorkingDir struct {
	events.WorkingDir
	err   error
	calls int
}

func (d *recoveryWorkingDir) Delete(logging.SimpleLogging, models.Repo, models.PullRequest) error {
	d.calls++
	return d.err
}

func recoveryAPI(t *testing.T) (*APIController, *sqlstore.Store, models.PullRequest, []models.Job, *recoveryWorkingDir) {
	t.Helper()
	store, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	t.Cleanup(func() { _ = store.Close() })
	pull := models.PullRequest{Num: -1, BaseRepo: models.Repo{FullName: "org/repo", VCSHost: models.VCSHost{Hostname: "git.example"}}, Execution: &models.Execution{Owner: "old-owner"}}
	Ok(t, store.AcquireClaim(pull, pull.ExecutionOwner()))
	pull.BaseRepo.Execution = pull.Execution
	var jobs []models.Job
	for _, name := range []string{"first", "second"} {
		job := models.Job{ID: uuid.NewString(), Pull: pull, Project: models.NewProject("org/repo", name, name), Workspace: "default", Command: "apply"}
		Ok(t, store.StartJob(job))
		acquired, _, err := store.TryLock(models.ProjectLock{Pull: pull, Project: job.Project, Workspace: job.Workspace})
		Ok(t, err)
		Assert(t, acquired, "project reservation was not acquired")
		jobs = append(jobs, job)
	}
	allowed, err := events.NewRepoAllowlistChecker("*")
	Ok(t, err)
	directory := &recoveryWorkingDir{}
	controller := &APIController{APISecret: []byte("secret"), Logger: logging.NewNoopLogger(t), Execution: &events.ExecutionCoordinator{Store: store, Drainer: &events.Drainer{}}, Locker: locking.NewClient(store), WorkingDir: directory, RepoAllowlistChecker: allowed}
	return controller, store, pull, jobs, directory
}

func unlockAPIRequest(t *testing.T, controller *APIController, id, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/jobs/"+id+"/unlock", strings.NewReader(body))
	r.Header.Set(atlantisTokenHeader, token)
	r = mux.SetURLVars(r, map[string]string{"job-id": id})
	w := httptest.NewRecorder()
	controller.UnlockJob(w, r)
	return w
}

func TestAPIJobUnlockReconcilesWholeInvocation(t *testing.T) {
	controller, store, pull, jobs, directory := recoveryAPI(t)
	Equals(t, http.StatusUnauthorized, unlockAPIRequest(t, controller, jobs[0].ID, "", `{"confirm_stopped":true}`).Code)
	Equals(t, http.StatusBadRequest, unlockAPIRequest(t, controller, jobs[0].ID, "secret", `{}`).Code)
	Equals(t, http.StatusConflict, unlockAPIRequest(t, controller, jobs[0].ID, "secret", `{"confirm_stopped":true}`).Code)
	Equals(t, 0, directory.calls)
	Ok(t, store.MarkUncertain(pull))
	response := unlockAPIRequest(t, controller, jobs[0].ID, "secret", `{"confirm_stopped":true}`)
	Assert(t, response.Code == http.StatusOK, "recovery failed: %s", response.Body.String())
	for _, old := range jobs {
		job, err := store.Job(old.ID)
		Ok(t, err)
		Equals(t, "interrupted", job.State)
		Equals(t, pull.Num, job.Pull.Num)
		Equals(t, pull.ExecutionOwner(), job.Owner)
	}
	locks, err := store.List()
	Ok(t, err)
	Equals(t, 0, len(locks))
	idle, err := store.PullIdle(pull)
	Ok(t, err)
	Assert(t, idle, "recovery claim leaked")
	Equals(t, http.StatusOK, unlockAPIRequest(t, controller, jobs[0].ID, "secret", `{"confirm_stopped":true}`).Code)
}

func TestAPIJobUnlockRetainsReservationsOnCleanupFailure(t *testing.T) {
	controller, store, pull, jobs, directory := recoveryAPI(t)
	Ok(t, store.MarkUncertain(pull))
	directory.err = errors.New("filesystem unavailable")
	Equals(t, http.StatusInternalServerError, unlockAPIRequest(t, controller, jobs[0].ID, "secret", `{"confirm_stopped":true}`).Code)
	locks, err := store.List()
	Ok(t, err)
	Equals(t, 2, len(locks))
	directory.err = nil
	Equals(t, http.StatusOK, unlockAPIRequest(t, controller, jobs[0].ID, "secret", `{"confirm_stopped":true}`).Code)
}

func TestAPIJobUnlockRejectsMalformedAndDisallowedRequests(t *testing.T) {
	controller, store, pull, jobs, _ := recoveryAPI(t)
	Ok(t, store.MarkUncertain(pull))
	for _, body := range []string{`{"confirm_stopped":false}`, `{"confirm_stopped":true,"actor":"spoofed"}`, `{"confirm_stopped":true} {}`, strings.Repeat(" ", 1025)} {
		Equals(t, http.StatusBadRequest, unlockAPIRequest(t, controller, jobs[0].ID, "secret", body).Code)
	}
	Equals(t, http.StatusNotFound, unlockAPIRequest(t, controller, uuid.NewString(), "secret", `{"confirm_stopped":true}`).Code)
	allowed, err := events.NewRepoAllowlistChecker("git.example/other/*")
	Ok(t, err)
	controller.RepoAllowlistChecker = allowed
	Equals(t, http.StatusForbidden, unlockAPIRequest(t, controller, jobs[0].ID, "secret", `{"confirm_stopped":true}`).Code)
	locks, err := store.List()
	Ok(t, err)
	Equals(t, 2, len(locks))
}

func TestAPIJobUnlockDoesNotRecoverAnotherInvocation(t *testing.T) {
	controller, store, pull, jobs, _ := recoveryAPI(t)
	for _, job := range jobs {
		Ok(t, store.FinishJob(job.ID, "failed", "failed"))
	}
	Ok(t, store.ReleaseClaim(pull))
	other := pull
	other.Num--
	other.Execution = &models.Execution{Owner: "another-invocation"}
	other.BaseRepo.Execution = other.Execution
	Ok(t, store.AcquireClaim(other, other.ExecutionOwner()))
	project := models.NewProject(other.BaseRepo.FullName, "another-project", "")
	acquired, _, err := store.TryLock(models.ProjectLock{Pull: other, Project: project, Workspace: "default"})
	Ok(t, err)
	Assert(t, acquired, "another invocation's reservation missing")
	otherJob := models.Job{ID: uuid.NewString(), Pull: other, Project: project, Workspace: "default", Command: "apply"}
	Ok(t, store.StartJob(otherJob))
	Ok(t, store.MarkUncertain(other))
	Equals(t, http.StatusOK, unlockAPIRequest(t, controller, jobs[0].ID, "secret", `{"confirm_stopped":true}`).Code)
	locks, err := store.List()
	Ok(t, err)
	Equals(t, 1, len(locks))
	Equals(t, other.Num, locks[0].Pull.Num)
	idle, err := store.PullIdle(other)
	Ok(t, err)
	Assert(t, !idle, "recovery released another invocation's ownership")
	unchanged, err := store.Job(otherJob.ID)
	Ok(t, err)
	Equals(t, "running", unchanged.State)
	Equals(t, other.ExecutionOwner(), unchanged.Owner)
}
