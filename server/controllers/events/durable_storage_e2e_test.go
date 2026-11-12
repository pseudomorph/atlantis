// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events_test

import (
	"bytes"
	"fmt"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/petergtz/pegomock/v4"
	"github.com/runatlantis/atlantis/server"
	events_controllers "github.com/runatlantis/atlantis/server/controllers/events"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/models"
	vcsmocks "github.com/runatlantis/atlantis/server/events/vcs/mocks"
	"github.com/runatlantis/atlantis/server/jobs"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

// durableE2E holds one server's durable-storage runtime. Reusing DataDir in a
// second setup restarts the server against the same database.
type durableE2E struct {
	DataDir   string
	Store     *sqlstore.Store
	Jobs      *jobs.Service
	Execution *events.ExecutionCoordinator
	Cleaner   *events.PullClosedExecutor
	closed    bool
}

func (d *durableE2E) open(t *testing.T) {
	t.Helper()
	store, err := sqlstore.OpenServer(d.DataDir)
	Ok(t, err)
	// Startup marks work left by a previous process uncertain, as the server does.
	Ok(t, store.MarkPreviousExecutionsUncertain())
	d.Store = store
	d.Jobs = &jobs.Service{Store: store}
	d.Execution = &events.ExecutionCoordinator{Store: store}
	t.Cleanup(func() { d.stop(t) })
}

// stop ends this server's use of storage without touching rows, which is what
// a crash or restart leaves behind.
func (d *durableE2E) stop(t *testing.T) {
	t.Helper()
	if d.closed {
		return
	}
	d.closed = true
	Ok(t, d.Jobs.Close())
	Ok(t, d.Store.Close())
}

// maintain runs one pass of the server's deferred closed-PR cleanup.
func (d *durableE2E) maintain(t *testing.T, logger logging.SimpleLogging) {
	t.Helper()
	ready, err := d.Store.ClosedPullsReady()
	Ok(t, err)
	for _, pull := range ready {
		Ok(t, d.Cleaner.CleanUpPull(logger, pull.BaseRepo, pull))
	}
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

func (s durableServer) close(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.ctrl.Post(w, GitHubPullRequestClosedEvent(t))
	return w
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

func pullWorkingDir(s durableServer) string {
	return filepath.Join(s.storage.DataDir, "repos")
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

func TestDurableStorageReopenedPullAcceptsCommands(t *testing.T) {
	skipUnlessDurableE2E(t)
	repoDir, headSHA := initializeRepo(t, "simple")
	s := startDurableServer(t, repoDir, headSHA, &durableE2E{})
	s.open(t, headSHA)
	lastReplyContains(t, s, "Plan: 3 to add")
	ResponseContains(t, s.close(t), 200, "Pull request cleaned successfully")
	before := len(s.replies())

	request := GitHubPullRequestOpenedEvent(t, headSHA)
	body, err := io.ReadAll(request.Body)
	Ok(t, err)
	request.Body = io.NopCloser(bytes.NewReader(bytes.Replace(body, []byte(`"action": "opened"`), []byte(`"action": "reopened"`), 1)))
	response := httptest.NewRecorder()
	s.ctrl.Post(response, request)
	ResponseContains(t, response, 200, "Processing...")
	Assert(t, len(s.replies()) == before+1, "reopened pull request did not autoplan")
	lastReplyContains(t, s, "Plan: 3 to add")
}

func TestDurableStorageCrashLeavesScopeBlocked(t *testing.T) {
	skipUnlessDurableE2E(t)
	repoDir, headSHA := initializeRepo(t, "simple")
	storage := &durableE2E{}
	first := startDurableServer(t, repoDir, headSHA, storage)
	first.open(t, headSHA)
	lastReplyContains(t, first, "Plan: 3 to add")

	// A claim row with no running process is what a crash mid-command leaves.
	pull, _, _, err := (&events.EventParser{GithubUser: "github-user", GithubToken: "github-token"}).ParseGithubPull(logging.NewNoopLogger(t), GitHubPullRequestParsed(headSHA))
	Ok(t, err)
	Ok(t, storage.Store.AcquireClaim(pull, "crashed-server"))
	storage.stop(t)

	restarted := startDurableServer(t, repoDir, headSHA, &durableE2E{DataDir: storage.DataDir})
	before := len(restarted.replies())
	jobsBefore, err := restarted.storage.Jobs.Store.Jobs()
	Ok(t, err)
	restarted.comment(t, "atlantis apply")
	restarted.comment(t, "atlantis plan")
	Assert(t, len(restarted.replies()) == before+2, "claim rejections were not reported")
	lastReplyContains(t, restarted, "uncertain")
	after, err := restarted.storage.Jobs.Store.Jobs()
	Ok(t, err)
	Equals(t, len(jobsBefore), len(after))
}

func TestDurableStorageCloseDefersCleanupWhileCommandRuns(t *testing.T) {
	skipUnlessDurableE2E(t)
	repoDir, headSHA := initializeRepo(t, "simple")
	s := startDurableServer(t, repoDir, headSHA, &durableE2E{})
	s.open(t, headSHA)
	lastReplyContains(t, s, "Plan: 3 to add")

	entered, release := make(chan struct{}), make(chan struct{})
	When(mockPreWorkflowHookRunner.Run(Any[models.WorkflowHookCommandContext](), Any[string](), Any[string](), Any[string](), Any[string]())).
		Then(func([]Param) ReturnValues {
			close(entered)
			<-release
			return ReturnValues{"", "", nil}
		})
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.comment(t, "atlantis plan")
	}()
	select {
	case <-entered:
	case <-time.After(time.Minute):
		t.Fatal("command never reached its pre-workflow hook")
	}

	// Closing while the command holds its claim must not delete its directory.
	w := s.close(t)
	Assert(t, strings.Contains(w.Body.String(), "cleanup deferred"), "close did not defer cleanup: %s", w.Body.String())
	Assert(t, len(pullDirs(t, s)) == 1, "close removed the working directory of a running command")

	close(release)
	<-done
	Assert(t, len(pullDirs(t, s)) == 1, "working directory removed before maintenance ran")
	s.storage.maintain(t, logging.NewNoopLogger(t))
	Assert(t, len(pullDirs(t, s)) == 0, "deferred cleanup left the working directory")
}

func TestDurableStorageCancelKeepsActiveOwnership(t *testing.T) {
	skipUnlessDurableE2E(t)
	repoDir, headSHA := initializeRepo(t, "simple")
	s := startDurableServer(t, repoDir, headSHA, &durableE2E{})
	s.open(t, headSHA)
	lastReplyContains(t, s, "Plan: 3 to add")
	before, err := s.storage.Store.Jobs()
	Ok(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	When(mockPreWorkflowHookRunner.Run(Any[models.WorkflowHookCommandContext](), Any[string](), Any[string](), Any[string](), Any[string]())).Then(func([]Param) ReturnValues {
		close(entered)
		<-release
		return ReturnValues{"", "", nil}
	})
	done := make(chan struct{})
	go func() { defer close(done); s.comment(t, "atlantis plan") }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("command did not enter its hook")
	}
	s.comment(t, "atlantis cancel")
	lastReplyContains(t, s, "Running operations will continue")
	pull, _, _, err := (&events.EventParser{GithubUser: "github-user", GithubToken: "github-token"}).ParseGithubPull(logging.NewNoopLogger(t), GitHubPullRequestParsed(headSHA))
	Ok(t, err)
	idle, err := s.storage.Store.PullIdle(pull)
	Ok(t, err)
	Assert(t, !idle, "cancel released active command ownership")
	s.comment(t, "atlantis apply")
	lastReplyContains(t, s, "already owned")
	unblock.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled command did not finish")
	}
	after, err := s.storage.Store.Jobs()
	Ok(t, err)
	Equals(t, len(before), len(after))
	idle, err = s.storage.Store.PullIdle(pull)
	Ok(t, err)
	Assert(t, idle, "completed cancellation left active ownership")
}

// pullDirs lists working directories for the test pull request (number 2).
func pullDirs(t *testing.T, s durableServer) []string {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(pullWorkingDir(s), "*", "runatlantis", "atlantis-tests", "2"))
	Ok(t, err)
	return dirs
}
