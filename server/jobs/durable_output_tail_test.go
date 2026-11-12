// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package jobs_test

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/core/logs"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/jobs"
	. "github.com/runatlantis/atlantis/testing"
)

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
			journal := &logs.Service{Records: store, Mode: mode, Directory: t.TempDir()}
			// Finish synchronously after returning an empty journal snapshot, before the
			// subscriber checks whether the stream was sealed.
			journal.Records = &finishAfterEmptyLogRead{LogStore: store, finish: func() {
				Ok(t, journal.Append(job.ID, []byte("last line\nunterminated tail")))
				Ok(t, store.FinishJob(job.ID, "succeeded", ""))
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
