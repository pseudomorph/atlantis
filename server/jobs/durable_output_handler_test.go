// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package jobs_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/artifacts"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/jobs"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

func TestDurableOutputSurvivesCleanupAndRestart(t *testing.T) {
	dataDir, artifactDir := t.TempDir(), t.TempDir()
	db, err := sqlstore.Open(dataDir)
	Ok(t, err)
	blobs, err := artifacts.NewFilesystem(artifactDir)
	Ok(t, err)
	service := &jobs.Service{Store: db, Artifacts: &artifacts.Service{Records: db, Blobs: blobs}}
	handler := &jobs.DurableOutputHandler{Service: service}
	repo := models.Repo{FullName: "org/repo", VCSHost: models.VCSHost{Hostname: "git.example"}}
	ctx := command.ProjectContext{JobID: uuid.NewString(), CommandName: command.Version, BaseRepo: repo, Pull: models.PullRequest{BaseRepo: repo, Num: 1}, RepoRelDir: ".", Workspace: "default", Log: logging.NewNoopLogger(t)}
	claimContext(t, service, &ctx)
	Ok(t, service.Begin(ctx))
	handler.Send(ctx, "line one", false)
	handler.Send(ctx, "line two", false)
	Ok(t, service.Finish(ctx, command.ProjectCommandOutput{}))
	handler.CleanUp(jobs.PullInfo{PullNum: 1, RepoFullName: repo.FullName})
	Ok(t, db.Close())
	Ok(t, blobs.Close())
	db, err = sqlstore.Open(dataDir)
	Ok(t, err)
	defer db.Close()
	blobs, err = artifacts.NewFilesystem(artifactDir)
	Ok(t, err)
	defer blobs.Close()
	handler = &jobs.DurableOutputHandler{Service: &jobs.Service{Store: db, Artifacts: &artifacts.Service{Records: db, Blobs: blobs}}}
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
