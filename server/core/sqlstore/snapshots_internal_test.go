// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestDerivedInputsKeepSelectedProducer(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	p.Execution = &models.Execution{Owner: "owner"}
	Ok(t, s.AcquireClaim(p, p.ExecutionOwner()))
	producer := claimJob(p, uuid.NewString())
	Ok(t, s.StartJob(producer))
	Ok(t, s.StartPlan(producer.ID))
	snapshot := validSnapshot(producer.ID)
	snapshot.Manifest.Files = append(snapshot.Manifest.Files,
		models.SnapshotFile{Kind: "lockfile", Path: ".terraform.lock.hcl", Size: 1, SHA256: snapshot.SHA256, Mode: 0600},
		models.SnapshotFile{Kind: "workflow", Path: "config.json", Size: 1, SHA256: snapshot.SHA256, Mode: 0600})
	Ok(t, s.BeginSnapshot(snapshot))
	Ok(t, s.CommitSnapshot(snapshot))
	changed := snapshot
	changed.SHA256 = validSnapshot(producer.ID).Manifest.Files[0].SHA256
	Assert(t, s.CommitSnapshot(changed) != nil, "published bundle was replaced")
	Ok(t, s.FinishJob(producer.ID, "succeeded", ""))
	Assert(t, s.BeginSnapshot(snapshot) != nil, "completed producer admitted a snapshot")

	consumer := claimJob(p, uuid.NewString())
	consumer.Command = "apply"
	Ok(t, s.StartJob(consumer))
	replacement := readyLifecyclePlan(t, s, p)
	Assert(t, s.StartJob(consumer) != nil, "existing consumer was replaced with a new producer")
	stored, err := s.Job(consumer.ID)
	Ok(t, err)
	Equals(t, producer.ID, stored.PlanJobID)
	input, err := s.InputSnapshot(consumer.ID)
	Ok(t, err)
	Assert(t, input != nil, "selected snapshot is missing")
	Equals(t, snapshot.JobID, input.JobID)
	Equals(t, snapshot.Manifest, input.Manifest)
	Equals(t, snapshot.SHA256, input.SHA256)
	ErrContains(t, "CHECK constraint failed", s.db.Exec("UPDATE plan_snapshots SET purged = TRUE WHERE job_id = ?", producer.ID).Error)
	Ok(t, s.FinishJob(consumer.ID, "succeeded", ""))
	pending, err := s.PendingPlans(p.BaseRepo, p.Num)
	Ok(t, err)
	Equals(t, 1, len(pending))
	Equals(t, replacement.ID, pending[0].ID)
	stored, err = s.Job(consumer.ID)
	Ok(t, err)
	Equals(t, producer.ID, stored.PlanJobID)
}

func TestSnapshotPublicationEndsWithProducer(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	p.Execution = &models.Execution{Owner: "owner"}
	Ok(t, s.AcquireClaim(p, p.ExecutionOwner()))
	producer := claimJob(p, uuid.NewString())
	Ok(t, s.StartJob(producer))
	Ok(t, s.StartPlan(producer.ID))
	snapshot := validSnapshot(producer.ID)
	Ok(t, s.BeginSnapshot(snapshot))
	Ok(t, s.FinishJob(producer.ID, "failed", "interrupted publication"))
	Assert(t, s.CommitSnapshot(snapshot) != nil, "completed producer published an unfinished snapshot")
	got, err := s.Snapshot(producer.ID)
	Ok(t, err)
	Assert(t, got == nil, "unfinished snapshot became visible")
}

func TestDerivedInputsProtectRunningConsumerRetention(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	p.Execution = &models.Execution{Owner: "owner"}
	Ok(t, s.AcquireClaim(p, p.ExecutionOwner()))
	unselected := readyLifecyclePlan(t, s, p)
	producer := readyLifecyclePlan(t, s, p)
	consumer := claimJob(p, uuid.NewString())
	consumer.Command = "apply"
	Ok(t, s.StartJob(consumer))
	Ok(t, s.ClosePull(p))

	// Isolate the consumer guard from claim and pending-plan protection.
	var claim executionClaim
	Ok(t, s.db.Where("owner = ?", p.ExecutionOwner()).Take(&claim).Error)
	Ok(t, s.db.Delete(&claim).Error)
	now := time.Now().Add(2 * time.Hour)
	expired, err := s.ExpiredSnapshots(now, time.Hour)
	Ok(t, err)
	Equals(t, 0, len(expired))
	expired, err = s.ExpiredSnapshots(now.Add(time.Hour), time.Hour)
	Ok(t, err)
	Equals(t, 1, len(expired))
	Equals(t, unselected.ID, expired[0].JobID)
	Ok(t, s.MarkSnapshotPurged(unselected.ID))
	input, err := s.InputSnapshot(consumer.ID)
	Ok(t, err)
	Assert(t, input != nil, "running consumer lost its snapshot")
	Equals(t, producer.ID, input.JobID)

	Ok(t, s.db.Create(&claim).Error)
	Ok(t, s.FinishJob(consumer.ID, "succeeded", ""))
	Ok(t, s.ReleaseClaim(p))
	expired, err = s.ExpiredSnapshots(now.Add(time.Hour), time.Hour)
	Ok(t, err)
	Equals(t, 0, len(expired))
	expired, err = s.ExpiredSnapshots(now.Add(2*time.Hour), time.Hour)
	Ok(t, err)
	Equals(t, 1, len(expired))
	Equals(t, producer.ID, expired[0].JobID)
}
