// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"time"

	"github.com/runatlantis/atlantis/server/events/models"
)

// SnapshotStore owns immutable plan metadata and consumer bindings.
type SnapshotStore interface {
	JobReader
	BeginSnapshot(models.PlanSnapshot) error
	CommitSnapshot(models.PlanSnapshot) error
	Snapshot(string) (*models.PlanSnapshot, error)
	InputSnapshot(string) (*models.PlanSnapshot, error)
	PendingPlans(models.Repo, int) ([]models.Job, error)
	InvalidatePlans(models.Repo, int) error
	ExpiredSnapshots(time.Time, time.Duration) ([]models.PlanSnapshot, error)
	MarkSnapshotPurged(string) error
	ReferencedSnapshots() (map[string]bool, bool, error)
}
