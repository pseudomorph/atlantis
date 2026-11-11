// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"strings"
	"testing"
	"time"

	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
	"gorm.io/gorm"
)

func TestDatabaseClock(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		s := conformanceStore(t)
		Ok(t, s.db.Transaction(func(tx *gorm.DB) error {
			before := time.Now()
			first, err := databaseNow(tx)
			after := time.Now()
			Ok(t, err)
			Equals(t, time.UTC, first.Location())
			Equals(t, 0, first.Nanosecond()%1000)
			Assert(t, !first.Before(before.Add(-time.Second)) && !first.After(after.Add(time.Second)), "database clock changed the current instant")
			time.Sleep(10 * time.Millisecond)
			second, err := databaseNow(tx)
			Ok(t, err)
			Assert(t, second.After(first), "database clock was frozen at transaction start")
			return nil
		}))
		tx := s.db.Begin()
		Ok(t, tx.Error)
		Ok(t, tx.Rollback().Error)
		_, err := databaseNow(tx)
		Assert(t, err != nil, "failed database clock silently used an application timestamp")
	})
}

func TestGeneratedStorageTimestamps(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		s := conformanceStore(t)
		// Missing timestamps must not fall through to GORM's application clock.
		s.db.NowFunc = func() time.Time { return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC) }
		before, err := s.Now()
		Ok(t, err)
		p := claimPull()
		Ok(t, s.AcquireClaim(p, "owner"))
		p.Execution = &models.Execution{Owner: "owner"}
		j := claimJob(p, "generated-times")
		j.Command, j.StartedAt = "version", time.Time{}
		Ok(t, s.StartJob(j))
		// Direct callers can omit lock time; the shared locking client supplies an event time.
		acquired, projectLock, err := s.TryLock(models.ProjectLock{Pull: p, Project: j.Project, Workspace: j.Workspace})
		Ok(t, err)
		Assert(t, acquired, "project lock unavailable")
		a := models.Artifact{ID: "generated-artifact", JobID: j.ID, Kind: "log", Location: "test/generated"}
		Ok(t, s.BeginArtifact(a))
		blobs := BlobStore{Store: s}
		meta, err := blobs.Put("test/generated", strings.NewReader("output"))
		Ok(t, err)
		a.Size, a.SHA256 = meta.Size, meta.SHA256
		Ok(t, s.CommitArtifact(a))
		objects, err := blobs.List()
		Ok(t, err)
		Equals(t, 1, len(objects))
		Ok(t, s.FinishJob(j.ID, "succeeded", ""))
		Ok(t, s.ClosePull(p))
		job, err := s.Job(j.ID)
		Ok(t, err)
		Assert(t, job.FinishedAt != nil, "job did not finish")
		artifacts, err := s.Artifacts(j.ID)
		Ok(t, err)
		Equals(t, 1, len(artifacts))
		var closed struct{ ClosedAt time.Time }
		Ok(t, s.db.Table("pulls").Select("closed_at").Where("id IN (?)", pullIDs(s.db, p.BaseRepo, p.Num)).Take(&closed).Error)
		after, err := s.Now()
		Ok(t, err)
		for name, instant := range map[string]time.Time{
			"job start": job.StartedAt, "job finish": *job.FinishedAt, "artifact": artifacts[0].CreatedAt,
			"blob": objects[0].ModifiedAt, "closure": closed.ClosedAt, "lock": projectLock.Time,
		} {
			Assert(t, !instant.Before(before) && !instant.After(after), "%s timestamp did not come from the database clock", name)
			Equals(t, 0, instant.Nanosecond()%1000)
		}
	})
}

func TestEqualJobTimestampsHaveStableOrder(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		s := conformanceStore(t)
		p := claimPull()
		Ok(t, s.AcquireClaim(p, "owner"))
		p.Execution = &models.Execution{Owner: "owner"}
		for _, id := range []string{"b", "a"} {
			j := claimJob(p, id)
			j.StartedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			j.Project = models.NewProject(p.BaseRepo.FullName, ".", id)
			Ok(t, s.StartJob(j))
			Ok(t, s.StartPlan(j.ID))
			a := models.Artifact{ID: id + "-plan", JobID: id, Kind: "plan", Location: id + "/plan", RelativePath: "plan.tfplan"}
			Ok(t, s.BeginArtifact(a))
			Ok(t, s.CommitArtifact(a))
			Ok(t, s.FinishJob(id, "succeeded", ""))
		}
		jobs, err := s.Jobs()
		Ok(t, err)
		plans, err := s.PendingPlans(p.BaseRepo, p.Num)
		Ok(t, err)
		Equals(t, []string{"a", "b"}, []string{jobs[0].ID, jobs[1].ID})
		Equals(t, []string{"a", "b"}, []string{plans[0].ID, plans[1].ID})
	})
}
