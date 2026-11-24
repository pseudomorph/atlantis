// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/events/models"
	"gorm.io/gorm"
)

var _ db.SnapshotStore = (*Store)(nil)

type planSnapshot struct {
	JobID                  string `gorm:"primaryKey"`
	Manifest               string
	Size                   int64
	SHA256                 string
	CreatedAt              time.Time
	Ready, Deleted, Purged bool
	UnreferencedAt         *time.Time
}

func (planSnapshot) TableName() string { return "plan_snapshots" }

func decodeSnapshot(row planSnapshot) (models.PlanSnapshot, error) {
	result := models.PlanSnapshot{JobID: row.JobID, Size: row.Size, SHA256: row.SHA256, CreatedAt: row.CreatedAt}
	err := json.Unmarshal([]byte(row.Manifest), &result.Manifest)
	if err == nil {
		err = result.Manifest.Validate()
	}
	return result, err
}

// BeginSnapshot reserves the producer identity before its single write starts.
func (s *Store) BeginSnapshot(want models.PlanSnapshot) error {
	if err := want.Manifest.Validate(); err != nil {
		return err
	}
	manifest, err := json.Marshal(want.Manifest)
	if err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		j, err := s.artifactJob(tx, want.JobID)
		if err != nil {
			return err
		}
		if j.Kind != models.CommandJob || j.Command != "plan" {
			return fmt.Errorf("only plan jobs can publish snapshots")
		}
		if _, hasPlan := want.Manifest.File("plan"); j.ManagedPlan && !hasPlan {
			return fmt.Errorf("managed plan snapshot is missing its plan file")
		}
		created := databaseTime(want.CreatedAt)
		if want.CreatedAt.IsZero() {
			created, err = databaseNow(tx)
			if err != nil {
				return err
			}
		}
		return tx.Create(&planSnapshot{JobID: want.JobID, Manifest: string(manifest), CreatedAt: created}).Error
	})
}

// CommitSnapshot publishes only the reserved manifest while its producer owns execution.
func (s *Store) CommitSnapshot(want models.PlanSnapshot) error {
	if err := want.Manifest.Validate(); err != nil {
		return err
	}
	hash, err := hex.DecodeString(want.SHA256)
	if err != nil || len(hash) != 32 || want.Size <= 0 {
		return fmt.Errorf("invalid plan snapshot content metadata")
	}
	manifest, err := json.Marshal(want.Manifest)
	if err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.artifactJob(tx, want.JobID); err != nil {
			return err
		}
		result := tx.Model(&planSnapshot{}).Where("job_id = ? AND manifest = ? AND ready = ? AND deleted = ?", want.JobID, string(manifest), false, false).Updates(map[string]any{"size": want.Size, "sha256": want.SHA256, "ready": true})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("snapshot publication intent is missing, changed, or already committed")
		}
		return nil
	})
}

func (s *Store) Snapshot(id string) (*models.PlanSnapshot, error) {
	var row planSnapshot
	err := s.db.Where("job_id = ? AND ready = ? AND deleted = ?", id, true, false).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result, err := decodeSnapshot(row)
	return &result, err
}

// InputSnapshot follows the immutable producer selected when the consumer was admitted.
func (s *Store) InputSnapshot(id string) (*models.PlanSnapshot, error) {
	var row planSnapshot
	producer := s.db.Model(&planBinding{}).Select("plan_job_id").Where("job_id = ?", id)
	err := s.db.Where("job_id IN (?) AND ready = ? AND deleted = ?", producer, true, false).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result, err := decodeSnapshot(row)
	return &result, err
}

// ExpiredSnapshots preserves selected plans and every running or uncertain consumer.
func (s *Store) ExpiredSnapshots(now time.Time, ttl time.Duration) ([]models.PlanSnapshot, error) {
	now = databaseTime(now)
	var out []models.PlanSnapshot
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var ids []string
		if err := tx.Model(&planSnapshot{}).Where("purged = ?", false).Order("job_id").Pluck("job_id", &ids).Error; err != nil {
			return err
		}
		// Lock each producer's pull in job ID order, then read the job and snapshot
		// under that lock so claims, consumers and publication cannot change.
		for _, id := range ids {
			var j job
			if err := tx.Where("id = ?", id).Take(&j).Error; err != nil {
				return err
			}
			if _, err := lockPull(tx, j.PullID); err != nil {
				return err
			}
			if err := tx.Where("id = ?", id).Take(&j).Error; err != nil {
				return err
			}
			var a planSnapshot
			err := tx.Where("job_id = ? AND purged = ?", id, false).Take(&a).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			var protected int64
			if err := tx.Model(&executionClaim{}).Where("pull_id = ?", j.PullID).Count(&protected).Error; err != nil {
				return err
			}
			if protected != 0 || j.State == "running" {
				continue
			}
			if err := tx.Model(&pendingPlan{}).Where("job_id = ?", j.ID).Count(&protected).Error; err != nil {
				return err
			}
			if protected != 0 {
				continue
			}
			if a.Ready {
				if err := tx.Model(&job{}).Joins("JOIN plan_bindings ON plan_bindings.job_id = jobs.id").Where("plan_bindings.plan_job_id = ? AND jobs.state = ?", j.ID, "running").Count(&protected).Error; err != nil {
					return err
				}
				if protected != 0 {
					continue
				}
			}
			if !a.Deleted {
				if a.UnreferencedAt == nil {
					a.UnreferencedAt = &now
					if err := tx.Model(&planSnapshot{}).Where("job_id = ?", a.JobID).Update("unreferenced_at", now).Error; err != nil {
						return err
					}
				}
				if now.Before(a.UnreferencedAt.Add(ttl)) {
					continue
				}
				if err := tx.Model(&planSnapshot{}).Where("job_id = ?", a.JobID).Update("deleted", true).Error; err != nil {
					return err
				}
			}
			decoded, err := decodeSnapshot(a)
			if err != nil {
				return err
			}
			out = append(out, decoded)
		}
		return nil
	})
	return out, err
}

func (s *Store) MarkSnapshotPurged(id string) error {
	return s.db.Model(&planSnapshot{}).Where("job_id = ? AND deleted = ?", id, true).Update("purged", true).Error
}

// ReferencedSnapshots reads quiescence before references. Snapshot rows are never
// deleted, so every reservation committed before the ID read is listed, and an
// uncommitted one belongs to a job the running count already saw. Reading IDs
// first would miss a producer that started and finished between the reads.
func (s *Store) ReferencedSnapshots() (map[string]bool, bool, error) {
	var active, running int64
	if err := s.db.Model(&executionClaim{}).Count(&active).Error; err != nil {
		return nil, false, err
	}
	if err := s.db.Model(&job{}).Where("state = ?", "running").Count(&running).Error; err != nil {
		return nil, false, err
	}
	var ids []string
	if err := s.db.Model(&planSnapshot{}).Pluck("job_id", &ids).Error; err != nil {
		return nil, false, err
	}
	refs := make(map[string]bool, len(ids))
	for _, id := range ids {
		refs[id] = true
	}
	return refs, active == 0 && running == 0, nil
}
