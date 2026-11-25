// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func logJob(t *testing.T, s *Store) models.Job {
	t.Helper()
	pull := claimPull()
	pull.Execution = &models.Execution{Owner: "log-owner"}
	Ok(t, s.AcquireClaim(pull, pull.ExecutionOwner()))
	job := claimJob(pull, uuid.NewString())
	job.Command = "version"
	Ok(t, s.StartJob(job))
	return job
}

func TestLogForeignKeysAndOffsets(t *testing.T) {
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) {
		s := d.Store(t)
		foreignKeyViolation(t, s.db.Exec("INSERT INTO job_logs(job_id) VALUES ('missing')").Error)
		foreignKeyViolation(t, s.db.Exec("INSERT INTO job_log_chunks(job_id,start_offset,content) VALUES ('missing',0,?)", []byte("orphan")).Error)
		job := logJob(t, s)
		Ok(t, s.AppendLogChunk(job.ID, []byte("first")))
		uniqueViolation(t, s.db.Exec("INSERT INTO job_log_chunks(job_id,start_offset,content) VALUES (?,0,?)", job.ID, []byte("duplicate")).Error)
		checkViolation(t, s.db.Exec("INSERT INTO job_log_chunks(job_id,start_offset,content) VALUES (?,-1,?)", job.ID, []byte("negative")).Error)
		Ok(t, s.MarkUncertain(job.Pull))
		Assert(t, s.AppendLogChunk(job.ID, []byte("stale")) != nil, "uncertain owner appended output")
		stream, data, err := s.ReadLogJournal(job.ID, 0, 1024)
		Ok(t, err)
		Equals(t, int64(5), stream.ByteLength)
		Equals(t, "first", string(data))
	})
}

func TestLogAppendAndArchiveHandoffAreAtomic(t *testing.T) {
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) {
		s := d.Store(t)
		job := logJob(t, s)
		Ok(t, s.AppendLogChunk(job.ID, []byte("first")))
		restore := injectFailure(t, s, "BEFORE INSERT", "job_log_chunks", "journal unavailable")
		ErrContains(t, "journal unavailable", s.AppendLogChunk(job.ID, []byte("second")))
		stream, data, err := s.ReadLogJournal(job.ID, 0, 1024)
		Ok(t, err)
		Equals(t, int64(5), stream.ByteLength)
		Equals(t, "first", string(data))
		restore()
		Ok(t, s.FinishJob(job.ID, "succeeded", ""))
		restore = injectFailure(t, s, "BEFORE DELETE", "job_log_chunks", "journal deletion unavailable")
		ErrContains(t, "journal deletion unavailable", s.MarkLogArchived(job.ID, 5, strings.Repeat("a", 64)))
		stream, data, err = s.ReadLogJournal(job.ID, 0, 1024)
		Ok(t, err)
		Assert(t, stream.ArchivedAt == nil, "archive marker committed without deleting journal atomically")
		Equals(t, "first", string(data))
		restore()
		Ok(t, s.MarkLogArchived(job.ID, 5, strings.Repeat("a", 64)))
		stream, data, err = s.ReadLogJournal(job.ID, 0, 1024)
		Ok(t, err)
		Assert(t, stream.ArchivedAt != nil, "archive marker missing")
		Equals(t, 0, len(data))
	})
}

func TestLogSealingAndJobCompletionAreAtomic(t *testing.T) {
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) {
		s := d.Store(t)
		job := logJob(t, s)
		Ok(t, s.AppendLogChunk(job.ID, []byte("output")))
		restore := injectFailure(t, s, "BEFORE UPDATE OF sealed_at", "job_logs", "sealing unavailable")
		ErrContains(t, "sealing unavailable", s.FinishJob(job.ID, "succeeded", ""))
		recorded, err := s.Job(job.ID)
		Ok(t, err)
		Equals(t, "running", recorded.State)
		stream, err := s.Log(job.ID)
		Ok(t, err)
		Assert(t, stream.SealedAt == nil, "failed completion sealed output")
		Assert(t, s.ReleaseClaim(job.Pull) != nil, "failed completion released ownership")
		restore()
		Ok(t, s.FinishJob(job.ID, "succeeded", ""))
	})
}
