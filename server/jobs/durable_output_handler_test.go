// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package jobs_test

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/artifacts"
	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/core/logs"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/jobs"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

func TestDurableOutputSurvivesCleanupAndRestart(t *testing.T) {
	dataDir, artifactDir := t.TempDir(), t.TempDir()
	db, err := sqlstore.OpenServer(dataDir)
	Ok(t, err)
	blobs, err := artifacts.NewFilesystem(artifactDir)
	Ok(t, err)
	service := &jobs.Service{Store: db, Logs: &logs.Service{Records: db}}
	handler := &jobs.DurableOutputHandler{Service: service}
	repo := models.Repo{FullName: "org/repo", VCSHost: models.VCSHost{Hostname: "git.example"}}
	ctx := command.ProjectContext{JobID: uuid.NewString(), CommandName: command.Version, BaseRepo: repo, Pull: models.PullRequest{BaseRepo: repo, Num: 1}, RepoRelDir: ".", Workspace: "default", Log: logging.NewNoopLogger(t)}
	claimContext(t, &fixture{Service: service}, &ctx)
	Ok(t, service.Begin(ctx))
	handler.Send(ctx, "line one", false)
	handler.Send(ctx, "line two", false)
	Ok(t, service.Finish(ctx, command.ProjectCommandOutput{}))
	handler.CleanUp(jobs.PullInfo{PullNum: 1, RepoFullName: repo.FullName})
	Ok(t, db.Close())
	Ok(t, blobs.Close())
	db, err = sqlstore.OpenServer(dataDir)
	Ok(t, err)
	defer db.Close()
	blobs, err = artifacts.NewFilesystem(artifactDir)
	Ok(t, err)
	defer blobs.Close()
	handler = &jobs.DurableOutputHandler{Service: &jobs.Service{Store: db, Logs: &logs.Service{Records: db}}}
	Assert(t, handler.IsKeyExists(ctx.JobID), "lost job on restart")
	receiver := make(chan string, 4)
	go handler.Register(ctx.JobID, receiver)
	var lines []string
	for {
		select {
		case line, ok := <-receiver:
			if !ok {
				Equals(t, []string{"line one", "line two"}, lines)
				return
			}
			lines = append(lines, line)
		case <-time.After(2 * time.Second):
			t.Fatal("replay did not finish")
		}
	}
}

type finishAfterEmptyLogRead struct {
	db.LogStore
	finish func()
	once   sync.Once
}

func (s *finishAfterEmptyLogRead) ReadLogJournal(id string, offset, limit int64) (*models.LogStream, []byte, error) {
	stream, data, err := s.LogStore.ReadLogJournal(id, offset, limit)
	if err == nil && stream != nil && len(data) == 0 {
		s.once.Do(s.finish)
	}
	return stream, data, err
}

func TestDurableOutputDrainsFinalFlushAfterEmptyRead(t *testing.T) {
	for _, mode := range []string{"database", "filesystem"} {
		t.Run(mode, func(t *testing.T) {
			store, err := sqlstore.OpenServer(t.TempDir())
			Ok(t, err)
			defer store.Close()
			pull := models.PullRequest{Num: 1, BaseRepo: models.Repo{FullName: "org/repo"}, Execution: &models.Execution{Owner: uuid.NewString()}}
			Ok(t, store.AcquireClaim(pull, pull.ExecutionOwner()))
			job := models.Job{ID: uuid.NewString(), Pull: pull, Kind: models.CommandJob, Command: "version", Project: models.NewProject(pull.BaseRepo.FullName, ".", ""), Workspace: "default"}
			Ok(t, store.StartJob(job))
			journal := &logs.Service{Records: store}
			if mode == "filesystem" {
				archive, err := artifacts.NewFilesystem(t.TempDir())
				Ok(t, err)
				defer archive.Close()
				journal.Archive = archive
			}
			// Finish synchronously after returning an empty journal snapshot, before the
			// subscriber checks whether the stream was sealed. With an archive, the
			// handoff also commits before the subscriber reads the final bytes.
			journal.Records = &finishAfterEmptyLogRead{LogStore: store, finish: func() {
				Ok(t, journal.Append(job.ID, []byte("last line\nunterminated tail")))
				Ok(t, store.FinishJob(job.ID, "succeeded", ""))
				Ok(t, journal.ArchiveSealed())
			}}
			handler := &jobs.DurableOutputHandler{Service: &jobs.Service{Store: store, Logs: journal}}
			receiver := make(chan string, 4)
			finished := make(chan struct{})
			go func() { defer close(finished); handler.Register(job.ID, receiver) }()
			select {
			case <-finished:
			case <-time.After(2 * time.Second):
				handler.Deregister(job.ID, receiver)
				<-finished
				t.Fatal("sealed output replay did not finish")
			}
			var lines []string
			for line := range receiver {
				lines = append(lines, line)
			}
			Equals(t, []string{"last line", "unterminated tail"}, lines)
		})
	}
}
