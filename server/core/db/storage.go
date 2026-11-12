// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"errors"

	"github.com/runatlantis/atlantis/server/events/models"
)

var ErrExecutionClaimLost = errors.New("execution claim lost")

// ErrSyntheticPullCollision rejects reuse of a historical API identity.
var ErrSyntheticPullCollision = errors.New("synthetic API pull number already reserved")

// ExecutionStore fences mutations while work owns a pull execution claim.
type ExecutionStore interface {
	AcquireClaim(models.PullRequest, string) error
	CheckClaim(models.PullRequest) error
	MarkUncertain(models.PullRequest) error
	ReleaseClaim(models.PullRequest) error
	CancelPull(models.PullRequest) error
	IsCancelled(models.PullRequest) (bool, error)
	ClosePull(models.PullRequest) error
	RequestReopen(models.PullRequest) error
	PullIdle(models.PullRequest) (bool, error)
	MarkClosedClean(models.PullRequest) error
}
