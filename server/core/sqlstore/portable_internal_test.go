// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestPortableSchemaConformance(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		t.Run("stable UUID relationships", func(t *testing.T) {
			s := conformanceStore(t)
			p := claimPull()
			Ok(t, s.AcquireClaim(p, "owner"))
			p.Execution = &models.Execution{Owner: "owner"}
			first := claimJob(p, uuid.NewString())
			Ok(t, s.StartJob(first))
			Ok(t, s.StartJob(claimJob(p, uuid.NewString())))
			for _, table := range []string{"vcs_instances", "repositories", "pulls", "projects"} {
				var ids []string
				Ok(t, s.db.Table(table).Pluck("id", &ids).Error)
				Equals(t, 1, len(ids))
				_, err := uuid.Parse(ids[0])
				Ok(t, err)
			}
			var stored job
			Ok(t, s.db.Where("id = ?", first.ID).Take(&stored).Error)
			other := p
			other.BaseRepo.FullName = "team/other"
			other.Execution = &models.Execution{Owner: "other-owner"}
			Ok(t, s.AcquireClaim(other, other.ExecutionOwner()))
			otherJob := claimJob(other, uuid.NewString())
			Ok(t, s.StartJob(otherJob))
			var otherStored job
			Ok(t, s.db.Where("id = ?", otherJob.ID).Take(&otherStored).Error)
			Assert(t, s.db.Exec("INSERT INTO locks(project_id,repository_id,pull_id,revision,username,created_at) VALUES (?,?,?,?,?,?)", stored.ProjectID, otherStored.RepositoryID, otherStored.PullID, "revision", "user", time.Now().UTC()).Error != nil, "cross-repository foreign key accepted")
		})
		t.Run("UTC timestamps and nullable completion", func(t *testing.T) {
			s := conformanceStore(t)
			previousLocation := time.Local
			time.Local = time.FixedZone("local-test", -7*60*60)
			t.Cleanup(func() { time.Local = previousLocation })
			p := claimPull()
			Ok(t, s.AcquireClaim(p, "owner"))
			p.Execution = &models.Execution{Owner: "owner"}
			Ok(t, s.CheckClaim(p))
			j := claimJob(p, uuid.NewString())
			j.StartedAt = time.Date(2026, 11, 5, 12, 30, 0, 123456789, time.FixedZone("offset", -7*60*60))
			Ok(t, s.StartJob(j))
			got, err := s.Job(j.ID)
			Ok(t, err)
			Assert(t, got.StartedAt.Equal(j.StartedAt.Truncate(time.Microsecond)), "job timestamp changed instant")
			Assert(t, got.FinishedAt == nil, "unfinished job has a completion timestamp")
			var matching int64
			Ok(t, s.db.Model(&job{}).Where("id = ? AND started_at = ?", j.ID, j.StartedAt.UTC().Truncate(time.Microsecond)).Count(&matching).Error)
			Equals(t, int64(1), matching)
			lock := models.ProjectLock{Pull: p, Project: j.Project, Workspace: j.Workspace, Time: j.StartedAt}
			acquired, _, err := s.TryLock(lock)
			Ok(t, err)
			Assert(t, acquired, "project lock unavailable")
			stored, err := s.GetLock(p.BaseRepo, j.Project, j.Workspace)
			Ok(t, err)
			Assert(t, stored.Time.Equal(j.StartedAt.Truncate(time.Microsecond)), "lock timestamp changed instant")
			a := models.Artifact{ID: uuid.NewString(), JobID: j.ID, Kind: "log", Location: "test/log", CreatedAt: j.StartedAt}
			Ok(t, s.BeginArtifact(a))
			Ok(t, s.CommitArtifact(a))
			rows, err := s.Artifacts(j.ID)
			Ok(t, err)
			Equals(t, 1, len(rows))
			Assert(t, rows[0].CreatedAt.Equal(j.StartedAt.Truncate(time.Microsecond)), "artifact timestamp changed instant")
			auto := a
			auto.ID, auto.Location, auto.CreatedAt = uuid.NewString(), "test/auto-log", time.Time{}
			Ok(t, s.BeginArtifact(auto))
			Ok(t, s.CommitArtifact(auto))
			var automatic models.Artifact
			Ok(t, s.db.Table("artifacts").Where("id = ?", auto.ID).Take(&automatic).Error)
			Assert(t, time.Since(automatic.CreatedAt).Abs() < time.Minute, "automatic timestamp changed instant")
			Assert(t, automatic.CreatedAt.Location() == time.UTC && automatic.CreatedAt.Nanosecond()%1000 == 0, "automatic time is not UTC microseconds")
			Ok(t, s.db.Table("artifacts").Where("id = ? AND created_at = ?", auto.ID, automatic.CreatedAt.UTC().Truncate(time.Microsecond)).Count(&matching).Error)
			Equals(t, int64(1), matching)
			Ok(t, s.FinishJob(j.ID, "succeeded", ""))
			got, err = s.Job(j.ID)
			Ok(t, err)
			Assert(t, got.FinishedAt != nil && time.Since(*got.FinishedAt).Abs() < time.Minute, "completion timestamp changed instant")
			Ok(t, s.ClosePull(p))
			Ok(t, s.ReleaseClaim(p))
			rows, err = s.ExpiredArtifacts(time.Now().Add(2*time.Hour), time.Hour, 0)
			Ok(t, err)
			Equals(t, 2, len(rows))
		})
		t.Run("lock listing follows creation time", func(t *testing.T) {
			s := conformanceStore(t)
			p := claimPull()
			Ok(t, s.AcquireClaim(p, "owner"))
			p.Execution = &models.Execution{Owner: "owner"}
			at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
			for i, directory := range []string{"second", "first"} {
				lock := models.ProjectLock{Pull: p, Project: models.NewProject(p.BaseRepo.FullName, directory, ""), Workspace: "default", Time: at.Add(-time.Duration(i) * time.Hour)}
				acquired, _, err := s.TryLock(lock)
				Ok(t, err)
				Assert(t, acquired, "project lock unavailable")
			}
			locks, err := s.List()
			Ok(t, err)
			Equals(t, 2, len(locks))
			Equals(t, "first", locks[0].Project.Path)
			Equals(t, "second", locks[1].Project.Path)
		})
		t.Run("artifact ordering and retention comparisons", func(t *testing.T) {
			s := conformanceStore(t)
			p := claimPull()
			Ok(t, s.AcquireClaim(p, "live"))
			p.Execution = &models.Execution{Owner: "live"}
			j := claimJob(p, uuid.NewString())
			Ok(t, s.StartJob(j))
			Ok(t, s.StartPlan(j.ID))
			at := time.Date(2026, 11, 5, 12, 0, 0, 987654321, time.UTC)
			a := models.Artifact{ID: uuid.NewString(), JobID: j.ID, Kind: "workflow", RelativePath: "custom.plan", Location: uuid.NewString(), CreatedAt: at}
			Ok(t, s.BeginArtifact(a))
			Ok(t, s.CommitArtifact(a))
			duplicate := a
			duplicate.ID = uuid.NewString()
			duplicate.Location = uuid.NewString()
			Assert(t, s.BeginArtifact(duplicate) != nil, "duplicate logical artifact accepted")
			latest := a.ID

			Ok(t, s.FinishJob(j.ID, "succeeded", ""))
			consumer := claimJob(p, uuid.NewString())
			consumer.Command = "apply"
			Ok(t, s.StartJob(consumer))
			inputs, err := s.InputArtifacts(consumer.ID)
			Ok(t, err)
			Equals(t, 1, len(inputs))
			Equals(t, latest, inputs[0].ID)
			Assert(t, inputs[0].CreatedAt.Equal(at.Truncate(time.Microsecond)), "artifact timestamp precision differs")
		})
		t.Run("binary values and booleans", func(t *testing.T) {
			s := conformanceStore(t)
			store := BlobStore{Store: s}
			allBytes := make([]byte, 256)
			for i := range allBytes {
				allBytes[i] = byte(i)
			}
			for _, payload := range [][]byte{{}, []byte("123.0"), allBytes} {
				meta, err := store.Put("test/object", bytes.NewReader(payload))
				Ok(t, err)
				Equals(t, fmt.Sprintf("%x", sha256.Sum256(payload)), meta.SHA256)
				r, err := store.Open("test/object")
				Ok(t, err)
				got, err := io.ReadAll(r)
				Ok(t, err)
				Ok(t, r.Close())
				Assert(t, bytes.Equal(payload, got), "binary payload changed")
			}
			objects, err := store.List()
			Ok(t, err)
			Equals(t, 1, len(objects))
			Assert(t, time.Since(objects[0].ModifiedAt).Abs() < time.Minute, "blob timestamp changed instant")
			p := claimPull()
			Ok(t, s.AcquireClaim(p, "owner"))
			p.Execution = &models.Execution{Owner: "owner"}
			j := claimJob(p, uuid.NewString())
			j.Private = true
			Ok(t, s.StartJob(j))
			got, err := s.Job(j.ID)
			Ok(t, err)
			Assert(t, got.Private, "boolean true did not round trip")
			var cancelled struct{ Closed, Cancelled bool }
			Ok(t, s.db.Table("pulls").Where("id IN (?)", pullIDs(s.db, p.BaseRepo, p.Num)).Take(&cancelled).Error)
			Assert(t, !cancelled.Closed && !cancelled.Cancelled, "false defaults did not round trip")
		})
	})
}
