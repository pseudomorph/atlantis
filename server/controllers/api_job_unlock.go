// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/runatlantis/atlantis/server/core/db"
)

// UnlockJob abandons the non-PR API invocation containing a durable job. It does
// not retry Terraform, unlock active processes, or create a replacement invocation.
func (a *APIController) UnlockJob(w http.ResponseWriter, r *http.Request) {
	middleware := a.getAPIMiddleware()
	if !middleware.RequireAuth(w, r) {
		return
	}
	responder := middleware.Responder
	if a.Execution == nil {
		responder.ServiceUnavailable(w, r, "durable execution recovery is disabled")
		return
	}
	id := mux.Vars(r)["job-id"]
	if _, err := uuid.Parse(id); err != nil {
		responder.Error(w, r, http.StatusBadRequest, NewAPIError(ErrCodeValidation, "job-id must be a UUID"))
		return
	}
	var request struct {
		ConfirmStopped bool `json:"confirm_stopped"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		responder.Error(w, r, http.StatusBadRequest, NewAPIError(ErrCodeValidation, "expected a JSON stopped-work attestation"))
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF || !request.ConfirmStopped {
		responder.Error(w, r, http.StatusBadRequest, NewAPIError(ErrCodeValidation, "confirm_stopped must be true after independently verifying all execution has stopped"))
		return
	}
	reader, ok := a.Execution.Store.(db.JobReader)
	if !ok {
		responder.ServiceUnavailable(w, r, "durable job recovery is unavailable")
		return
	}
	job, err := reader.Job(id)
	if err != nil {
		responder.InternalError(w, r, err)
		return
	}
	if job == nil {
		responder.NotFound(w, r, "job not found")
		return
	}
	if job.Pull.Num >= 0 {
		responder.Error(w, r, http.StatusConflict, NewAPIError(ErrCodeConflict, "use atlantis unlock on the associated PR"))
		return
	}
	if a.RepoAllowlistChecker == nil || !a.RepoAllowlistChecker.IsAllowlisted(job.Pull.BaseRepo.FullName, job.Pull.BaseRepo.VCSHost.Hostname) {
		responder.Forbidden(w, r, "repository is not allowlisted")
		return
	}
	pull, release, err := a.Execution.RecoverAPIJob(id, "api-token", a.Logger)
	if err != nil {
		responder.Error(w, r, http.StatusConflict, NewAPIError(ErrCodeConflict, err.Error()))
		return
	}
	defer release()
	if err := a.WorkingDir.Delete(a.Logger, pull.BaseRepo, pull); err != nil {
		responder.InternalError(w, r, err)
		return
	}
	locks, err := a.Locker.UnlockByPull(pull.BaseRepo, pull.Num)
	if err != nil {
		responder.InternalError(w, r, err)
		return
	}
	responder.Success(w, r, http.StatusOK, map[string]any{"job_id": id, "released_reservations": len(locks)})
}
