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
	var expected []string
	var unfinished models.Artifact
	for _, kind := range []string{"plan", "lockfile", "workflow", "log", "unfinished"} {
		a := models.Artifact{ID: uuid.NewString(), JobID: producer.ID, Kind: kind, Location: "test/" + uuid.NewString()}
		if kind == "unfinished" {
			a.Kind, a.RelativePath = "workflow", "unfinished"
		}
		Ok(t, s.BeginArtifact(a))
		if kind == "unfinished" {
			unfinished = a
			continue
		}
		Ok(t, s.CommitArtifact(a))
		if kind != "log" {
			expected = append(expected, a.ID)
		}
	}
	Ok(t, s.FinishJob(producer.ID, "succeeded", ""))
	Assert(t, s.CommitArtifact(unfinished) != nil, "completed producer published an unfinished artifact")
	late := models.Artifact{ID: uuid.NewString(), JobID: producer.ID, Kind: "workflow", Location: "test/late"}
	Assert(t, s.BeginArtifact(late) != nil, "completed producer admitted a new artifact")

	consumer := claimJob(p, uuid.NewString())
	consumer.Command = "apply"
	Ok(t, s.StartJob(consumer))
	replacement := readyLifecyclePlan(t, s, p)
	Assert(t, s.StartJob(consumer) != nil, "existing consumer was replaced with a new producer")
	stored, err := s.Job(consumer.ID)
	Ok(t, err)
	Equals(t, producer.ID, stored.PlanJobID)
	inputs, err := s.InputArtifacts(consumer.ID)
	Ok(t, err)
	var actual []string
	for _, a := range inputs {
		Equals(t, producer.ID, a.JobID)
		actual = append(actual, a.ID)
	}
	Equals(t, expected, actual)
	ErrContains(t, "CHECK constraint failed", s.db.Exec("UPDATE artifacts SET purged = TRUE WHERE id = ?", expected[0]).Error)
	Ok(t, s.FinishJob(consumer.ID, "succeeded", ""))
	pending, err := s.PendingPlans(p.BaseRepo, p.Num)
	Ok(t, err)
	Equals(t, 1, len(pending))
	Equals(t, replacement.ID, pending[0].ID)
	stored, err = s.Job(consumer.ID)
	Ok(t, err)
	Equals(t, producer.ID, stored.PlanJobID)
}

func TestDerivedInputsProtectRunningConsumerRetention(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	p.Execution = &models.Execution{Owner: "owner"}
	Ok(t, s.AcquireClaim(p, p.ExecutionOwner()))
	unselected := readyLifecyclePlan(t, s, p)
	unselectedArtifacts, err := s.Artifacts(unselected.ID)
	Ok(t, err)
	Equals(t, 1, len(unselectedArtifacts))
	producer := claimJob(p, uuid.NewString())
	Ok(t, s.StartJob(producer))
	Ok(t, s.StartPlan(producer.ID))
	plan := models.Artifact{ID: uuid.NewString(), JobID: producer.ID, Kind: "plan", Location: "test/plan"}
	log := models.Artifact{ID: uuid.NewString(), JobID: producer.ID, Kind: "log", Location: "test/log"}
	for _, a := range []models.Artifact{plan, log} {
		Ok(t, s.BeginArtifact(a))
		Ok(t, s.CommitArtifact(a))
	}
	unpublished := models.Artifact{ID: uuid.NewString(), JobID: producer.ID, Kind: "workflow", Location: "test/unpublished"}
	Ok(t, s.BeginArtifact(unpublished))
	Ok(t, s.FinishJob(producer.ID, "succeeded", ""))
	consumer := claimJob(p, uuid.NewString())
	consumer.Command = "apply"
	Ok(t, s.StartJob(consumer))
	Ok(t, s.ClosePull(p))

	// Isolate the consumer guard from claim and pending-plan protection.
	var claim executionClaim
	Ok(t, s.db.Where("owner = ?", p.ExecutionOwner()).Take(&claim).Error)
	Ok(t, s.db.Delete(&claim).Error)
	now := time.Now().Add(2 * time.Hour)
	expired, err := s.ExpiredArtifacts(now, time.Hour, 0)
	Ok(t, err)
	Equals(t, 3, len(expired))
	for _, a := range expired {
		Assert(t, a.ID == log.ID || a.ID == unpublished.ID || a.ID == unselectedArtifacts[0].ID, "running consumer lost its plan")
		Ok(t, s.MarkPurged(a.ID))
	}
	inputs, err := s.InputArtifacts(consumer.ID)
	Ok(t, err)
	Equals(t, 1, len(inputs))
	Equals(t, plan.ID, inputs[0].ID)

	Ok(t, s.db.Create(&claim).Error)
	Ok(t, s.FinishJob(consumer.ID, "succeeded", ""))
	Ok(t, s.ReleaseClaim(p))
	expired, err = s.ExpiredArtifacts(now, time.Hour, 0)
	Ok(t, err)
	Equals(t, 1, len(expired))
	Equals(t, plan.ID, expired[0].ID)
}
