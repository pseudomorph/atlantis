// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package logs_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/artifacts"
	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/core/logs"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func runningJob(t *testing.T, store *sqlstore.Store, number int) models.Job {
	t.Helper()
	repo := models.Repo{FullName: "org/repo", VCSHost: models.VCSHost{Hostname: "git.example"}}
	pull := models.PullRequest{Num: number, BaseRepo: repo, HeadCommit: "revision", Execution: &models.Execution{Owner: uuid.NewString()}}
	pull.BaseRepo.Execution = pull.Execution
	Ok(t, store.AcquireClaim(pull, pull.ExecutionOwner()))
	job := models.Job{ID: uuid.NewString(), Kind: models.CommandJob, Command: "version", Pull: pull, Project: models.NewProject(repo.FullName, ".", ""), Workspace: "default"}
	Ok(t, store.StartJob(job))
	return job
}

func setup(t *testing.T) (*sqlstore.Store, *logs.Service, models.Job) {
	t.Helper()
	store, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	t.Cleanup(func() { _ = store.Close() })
	job := runningJob(t, store, 1)
	return store, &logs.Service{Records: store, Mode: "database"}, job
}

func TestJournalOffsetsAndCompletion(t *testing.T) {
	store, journal, job := setup(t)
	Ok(t, journal.Append(job.ID, []byte("first\n")))
	Ok(t, journal.Append(job.ID, []byte("second\n")))
	data, offset, err := journal.ReadAfter(job.ID, 3)
	Ok(t, err)
	Equals(t, "st\nsecond\n", string(data))
	Equals(t, int64(13), offset)
	info, err := journal.Info(job.ID)
	Ok(t, err)
	Assert(t, info.SealedAt == nil, "running output sealed")
	Ok(t, store.FinishJob(job.ID, "succeeded", ""))
	info, err = journal.Info(job.ID)
	Ok(t, err)
	Assert(t, info.SealedAt != nil && !info.Interrupted, "completion did not seal output")
	Assert(t, journal.Append(job.ID, []byte("late")) != nil, "completed writer appended output")
}

type uploadFailure struct{ artifacts.Store }

func (uploadFailure) Put(string, io.Reader) (artifacts.Metadata, error) {
	return artifacts.Metadata{}, fmt.Errorf("S3 unavailable")
}

type acknowledgementFailure struct{ db.LogStore }

func (acknowledgementFailure) MarkLogArchived(string, int64, string) error {
	return fmt.Errorf("archive acknowledgement unavailable")
}

func TestArchiveFailureAndHandoffPreserveOffsets(t *testing.T) {
	for _, failure := range []string{"upload", "acknowledgement"} {
		t.Run(failure, func(t *testing.T) {
			store, journal, job := setup(t)
			first := bytes.Repeat([]byte("x"), 700*1024)
			second := bytes.Repeat([]byte("y"), 700*1024)
			Ok(t, journal.Append(job.ID, first))
			Ok(t, journal.Append(job.ID, second))
			Ok(t, store.FinishJob(job.ID, "succeeded", ""))
			archive, err := artifacts.NewFilesystem(t.TempDir())
			Ok(t, err)
			defer archive.Close()
			journal.Mode, journal.Archive = "s3", archive
			if failure == "upload" {
				journal.Archive = uploadFailure{archive}
			} else {
				journal.Records = acknowledgementFailure{store}
			}
			Assert(t, journal.ArchiveSealed() != nil, "injected archival failure ignored")
			info, err := store.Log(job.ID)
			Ok(t, err)
			Assert(t, info.ArchivedAt == nil, "failed archival became visible")
			_, retained, err := store.ReadLogJournal(job.ID, 0, int64(len(first)+len(second)))
			Ok(t, err)
			Equals(t, len(first)+len(second), len(retained))
			completed, err := store.Job(job.ID)
			Ok(t, err)
			Equals(t, "succeeded", completed.State)
			journal.Records, journal.Archive = store, archive
			prefix, cursor, err := journal.ReadAfter(job.ID, 0)
			Ok(t, err)
			Ok(t, journal.ArchiveSealed())
			suffix, end, err := journal.ReadAfter(job.ID, cursor)
			Ok(t, err)
			Equals(t, int64(len(first)+len(second)), end)
			Equals(t, append(first, second...), append(prefix, suffix...))
			info, remaining, err := store.ReadLogJournal(job.ID, 0, int64(len(first)+len(second)))
			Ok(t, err)
			Assert(t, info.ArchivedAt != nil && len(info.ArchiveSHA256) == 64, "archive handoff missing")
			Equals(t, 0, len(remaining))
			data, offset, err := journal.ReadAfter(job.ID, int64(len(first)-3))
			Ok(t, err)
			Equals(t, "xxxy", string(data[:4]))
			Equals(t, int64(len(first)+len(second)), offset)
			all, err := journal.ReadAll(job.ID)
			Ok(t, err)
			Equals(t, append(first, second...), all)
			Ok(t, journal.ArchiveSealed())
		})
	}
}

func TestRestartSealsCapturedPrefixWithoutRecoveringExecution(t *testing.T) {
	directory := t.TempDir()
	store, err := sqlstore.OpenServer(directory)
	Ok(t, err)
	job := runningJob(t, store, -7)
	journal := &logs.Service{Records: store, Mode: "database"}
	Ok(t, journal.Append(job.ID, []byte("captured prefix\n")))
	Ok(t, store.Close())
	store, err = sqlstore.OpenServer(directory)
	Ok(t, err)
	defer store.Close()
	Ok(t, store.MarkPreviousExecutionsUncertain())
	archive, err := artifacts.NewFilesystem(t.TempDir())
	Ok(t, err)
	defer archive.Close()
	journal = &logs.Service{Records: store, Mode: "s3", Archive: archive}
	info, err := journal.Info(job.ID)
	Ok(t, err)
	Assert(t, info.Interrupted && info.SealedAt != nil, "old output stream was left writable")
	Assert(t, store.CheckClaim(job.Pull) != nil, "log recovery recovered execution ownership")
	Ok(t, journal.ArchiveSealed())
	data, err := journal.ReadAll(job.ID)
	Ok(t, err)
	Equals(t, "captured prefix\n", string(data))
	stored, err := store.Job(job.ID)
	Ok(t, err)
	Equals(t, "running", stored.State)
}

func TestFilesystemBypassesJournalAndRejectsEscapingSymlink(t *testing.T) {
	store, journal, job := setup(t)
	journal.Mode, journal.Directory = "filesystem", t.TempDir()
	Ok(t, journal.Append(job.ID, []byte("file output\n")))
	info, chunks, err := store.ReadLogJournal(job.ID, 0, 1024)
	Ok(t, err)
	Equals(t, int64(12), info.ByteLength)
	Equals(t, 0, len(chunks))
	Ok(t, store.FinishJob(job.ID, "succeeded", ""))
	data, err := journal.ReadAll(job.ID)
	Ok(t, err)
	Equals(t, "file output\n", string(data))
	other := runningJob(t, store, 2)
	outside := t.TempDir()
	escaped := &logs.Service{Records: store, Mode: "filesystem", Directory: t.TempDir()}
	Ok(t, os.Symlink(outside, filepath.Join(escaped.Directory, "logs")))
	Assert(t, escaped.Append(other.ID, []byte("secret")) != nil, "log escaped its filesystem root")
	entries, err := os.ReadDir(outside)
	Ok(t, err)
	Equals(t, 0, len(entries))
}

func TestRetentionPreservesUncertainLogsAndClosureAnchor(t *testing.T) {
	store, journal, job := setup(t)
	Ok(t, journal.Append(job.ID, []byte("kept")))
	Ok(t, store.ClosePull(job.Pull))
	Ok(t, store.MarkUncertain(job.Pull))
	Ok(t, journal.Collect(time.Now().Add(24*time.Hour), time.Hour))
	data, err := journal.ReadAll(job.ID)
	Ok(t, err)
	Equals(t, "kept", string(data))
	Ok(t, store.AcquireUnlockClaim(job.Pull, "recovery", "operator", true))
	pull := job.Pull
	pull.Execution = &models.Execution{Owner: "recovery"}
	Ok(t, store.ReleaseClaim(pull))
	Ok(t, store.RequestReopen(job.Pull))
	Ok(t, store.MarkClosedClean(job.Pull))
	Ok(t, store.AcquireClaim(job.Pull, "new-owner"))
	pull.Execution = &models.Execution{Owner: "new-owner"}
	Ok(t, store.ReleaseClaim(pull))
	Ok(t, journal.Collect(time.Now().Add(24*time.Hour), time.Hour))
	data, err = journal.ReadAll(job.ID)
	Ok(t, err)
	Equals(t, "", string(data))
	info, err := journal.Info(job.ID)
	Ok(t, err)
	Assert(t, info.PurgedAt != nil, "expired log was not purged")
}

func TestChunkOrphansAndDuplicateOffsetsRejected(t *testing.T) {
	store, journal, job := setup(t)
	Ok(t, journal.Append(job.ID, []byte(strings.Repeat("z", 1024))))
	Assert(t, store.CommitFileLog(job.ID, 0, 1) != nil, "stale acknowledgement advanced output")
	Assert(t, journal.Append("missing", []byte("orphan")) != nil, "missing job accepted output")
}

func TestArchivedReadAllRejectsSameLengthCorruption(t *testing.T) {
	store, journal, job := setup(t)
	Ok(t, journal.Append(job.ID, []byte("original")))
	Ok(t, store.FinishJob(job.ID, "succeeded", ""))
	archive, err := artifacts.NewFilesystem(t.TempDir())
	Ok(t, err)
	defer archive.Close()
	journal.Mode, journal.Archive = "s3", archive
	Ok(t, journal.ArchiveSealed())
	_, err = archive.Put(logs.Key(job.ID), strings.NewReader("tampered"))
	Ok(t, err)
	_, err = journal.ReadAll(job.ID)
	ErrContains(t, "checksum mismatch", err)
}
