// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"errors"
	"time"

	"github.com/runatlantis/atlantis/server/events/models"
)

var ErrExecutionClaimLost = errors.New("execution claim lost")

// ExecutionStore fences mutations while work owns a repository execution scope.
type ExecutionStore interface {
	AcquireClaim(models.PullRequest, string) error
	CheckClaim(models.PullRequest) error
	MarkUncertain(models.PullRequest) error
	ReleaseClaim(models.PullRequest) error
	CancelPull(models.PullRequest) error
	IsCancelled(models.PullRequest) (bool, error)
	ClosePull(models.PullRequest) error
	PullIdle(models.PullRequest) (bool, error)
}

type JobReader interface {
	Job(string) (*models.Job, error)
	Jobs() ([]models.Job, error)
}

type JobStore interface {
	JobReader
	StartJob(models.Job) error
	StartPlan(string) error
	MarkJobMutating(string) error
	FinishJob(id, state, message string) error
}

// ArtifactStore owns metadata and input bindings; byte backends never select plans.
type ArtifactStore interface {
	JobReader
	BeginArtifact(models.Artifact) error
	CommitArtifact(models.Artifact) error
	InputArtifacts(string) ([]models.Artifact, error)
	LogArtifactsAfter(string, int64) ([]models.Artifact, error)
	Artifacts(string) ([]models.Artifact, error)
	PendingPlans(models.Repo, int) ([]models.Job, error)
	InvalidatePlans(models.Repo, int) error
	ExpiredArtifacts(time.Time, time.Duration, time.Duration) ([]models.Artifact, error)
	MarkPurged(string) error
	ReferencedObjects() (map[string]bool, bool, error)
}
