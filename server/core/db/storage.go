// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"errors"

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
