// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events_test

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/petergtz/pegomock/v4"
	"github.com/runatlantis/atlantis/server"
	events_controllers "github.com/runatlantis/atlantis/server/controllers/events"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/models"
	vcsmocks "github.com/runatlantis/atlantis/server/events/vcs/mocks"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

// durableE2E holds one server's durable storage. Reusing DataDir in a second
// setup restarts the server against the same database.
type durableE2E struct {
	DataDir string
	Store   *sqlstore.Store
	closed  bool
}

func (d *durableE2E) open(t *testing.T) {
	t.Helper()
	store, err := sqlstore.OpenServer(d.DataDir)
	Ok(t, err)
	d.Store = store
	t.Cleanup(func() { d.stop(t) })
}

// stop ends this server's use of storage without touching rows, which is what
// a restart leaves behind.
func (d *durableE2E) stop(t *testing.T) {
	t.Helper()
	if d.closed {
		return
	}
	d.closed = true
	Ok(t, d.Store.Close())
}

type durableServer struct {
	ctrl      events_controllers.VCSEventsController
	vcs       *vcsmocks.MockClient
	workspace *events.FileWorkspace
	storage   *durableE2E
}

func startDurableServer(t *testing.T, repoDir, headSHA string, storage *durableE2E) durableServer {
	t.Helper()
	userConfig.EnablePolicyChecksFlag = false
	ctrl, vcsClient, githubGetter, workspace := setupE2E(t, "simple", setupOption{durable: storage})
	workspace.TestingOverrideHeadCloneURL = fmt.Sprintf("file://%s", repoDir)
	When(githubGetter.GetPullRequest(Any[logging.SimpleLogging](), Any[models.Repo](), Any[int]())).ThenReturn(GitHubPullRequestParsed(headSHA), nil)
	When(vcsClient.GetModifiedFiles(Any[logging.SimpleLogging](), Any[models.Repo](), Any[models.PullRequest]())).ThenReturn([]string{"main.tf"}, nil)
	return durableServer{ctrl: ctrl, vcs: vcsClient, workspace: workspace, storage: storage}
}

func (s durableServer) comment(t *testing.T, body string) {
	t.Helper()
	w := httptest.NewRecorder()
	s.ctrl.Post(w, GitHubCommentEvent(t, body))
	ResponseContains(t, w, 200, "Processing...")
}

func (s durableServer) open(t *testing.T, headSHA string) {
	t.Helper()
	w := httptest.NewRecorder()
	s.ctrl.Post(w, GitHubPullRequestOpenedEvent(t, headSHA))
	ResponseContains(t, w, 200, "Processing...")
}

// replies returns every comment the server posted, in order.
func (s durableServer) replies() []string {
	_, _, _, comments, _ := s.vcs.VerifyWasCalled(AtLeast(0)).CreateComment(
		Any[logging.SimpleLogging](), Any[models.Repo](), Any[int](), Any[string](), Any[string]()).GetAllCapturedArguments()
	return comments
}

func lastReplyContains(t *testing.T, s durableServer, want string) {
	t.Helper()
	replies := s.replies()
	Assert(t, len(replies) > 0, "server posted no comments")
	last := replies[len(replies)-1]
	Assert(t, strings.Contains(last, want), "last comment does not contain %q:\n%s", want, last)
}

func skipUnlessDurableE2E(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.SkipNow()
	}
	ensureRunning014(t)
	RegisterMockTestingT(t)
	userConfig = server.UserConfig{}
}

// Locks and pull status live in SQLite, so a restarted server applies the plan
// that the first server recorded.
func TestDurableStoragePlanSurvivesRestart(t *testing.T) {
	skipUnlessDurableE2E(t)
	repoDir, headSHA := initializeRepo(t, "simple")
	storage := &durableE2E{}
	first := startDurableServer(t, repoDir, headSHA, storage)
	first.open(t, headSHA)
	lastReplyContains(t, first, "Plan: 3 to add")
	locks, err := storage.Store.List()
	Ok(t, err)
	Equals(t, 1, len(locks))
	first.storage.stop(t)

	restarted := startDurableServer(t, repoDir, headSHA, &durableE2E{DataDir: storage.DataDir})
	locks, err = restarted.storage.Store.List()
	Ok(t, err)
	Equals(t, 1, len(locks))
	restarted.comment(t, "atlantis apply")
	lastReplyContains(t, restarted, "Apply complete!")
	locks, err = restarted.storage.Store.List()
	Ok(t, err)
	Equals(t, 1, len(locks))
}
