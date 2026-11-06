// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"time"

	"github.com/runatlantis/atlantis/server/events/models"

	"gorm.io/gorm"
)

// ExpiredArtifacts tombstones only objects with no active or uncertain execution,
// pending plan, or running consumer. Records and their relationships survive.
func (s *Store) ExpiredArtifacts(now time.Time, logs, planArtifacts time.Duration) ([]models.Artifact, error) {
	now = databaseTime(now)
	var out []models.Artifact
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var rows []artifact
		if err := tx.Table("artifacts").Where("purged = ?", false).Find(&rows).Error; err != nil {
			return err
		}
		for _, a := range rows {
			var j job
			if err := tx.Where("id = ?", a.JobID).Take(&j).Error; err != nil {
				return err
			}
			var active int64
			if err := tx.Model(&executionClaim{}).Where("pull_id = ?", j.PullID).Count(&active).Error; err != nil {
				return err
			}
			if active != 0 || j.State == "running" {
				continue
			}
			if err := tx.Model(&pendingPlan{}).Where("job_id = ?", j.ID).Count(&active).Error; err != nil {
				return err
			}
			if active != 0 {
				continue
			}
			if err := tx.Table("artifact_inputs").Where("artifact_id = ? AND job_id IN (?)", a.ID, tx.Model(&job{}).Select("id").Where("state = ?", "running")).Count(&active).Error; err != nil {
				return err
			}
			if active != 0 {
				continue
			}
			if !a.Deleted {
				if a.Kind == "log" {
					var p struct {
						Number   int
						Closed   bool
						ClosedAt *time.Time
					}
					if err := tx.Table("pulls").Where("id = ?", j.PullID).Take(&p).Error; err != nil {
						return err
					}
					start := j.FinishedAt
					if p.Number > 0 {
						if !p.Closed {
							continue
						}
						start = p.ClosedAt
					}
					if start == nil || now.Before(start.Add(logs)) {
						continue
					}
				} else {
					if a.UnreferencedAt == nil {
						a.UnreferencedAt = &now
						if err := tx.Table("artifacts").Where("id = ?", a.ID).Update("unreferenced_at", now).Error; err != nil {
							return err
						}
					}
					if now.Before(a.UnreferencedAt.Add(planArtifacts)) {
						continue
					}
				}
				if err := tx.Table("artifacts").Where("id = ?", a.ID).Update("deleted", true).Error; err != nil {
					return err
				}
				a.Deleted = true
			}
			out = append(out, a.Artifact)
		}
		return nil
	})
	return out, err
}
func (s *Store) MarkPurged(id string) error {
	return s.db.Table("artifacts").Where("id = ? AND deleted = ?", id, true).Update("purged", true).Error
}
func (s *Store) ReferencedObjects() (map[string]bool, bool, error) {
	var paths []string
	if err := s.db.Table("artifacts").Pluck("location", &paths).Error; err != nil {
		return nil, false, err
	}
	var active int64
	if err := s.db.Model(&executionClaim{}).Count(&active).Error; err != nil {
		return nil, false, err
	}
	var running int64
	if err := s.db.Model(&job{}).Where("state = ?", "running").Count(&running).Error; err != nil {
		return nil, false, err
	}
	refs := map[string]bool{}
	for _, p := range paths {
		refs[p] = true
	}
	return refs, active == 0 && running == 0, nil
}
