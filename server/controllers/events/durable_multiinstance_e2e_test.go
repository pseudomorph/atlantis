// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/petergtz/pegomock/v4"
	"github.com/runatlantis/atlantis/server/core/sqlstore/sqlstoretest"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

// startSharedServers starts two servers, each with its own data directory, on
// one PostgreSQL database. It skips without ATLANTIS_TEST_POSTGRES_DSN.
func startSharedServers(t *testing.T, repoDir, headSHA string) (durableServer, durableServer) {
	t.Helper()
	skipUnlessDurableE2E(t)
	dsn := sqlstoretest.PostgresDSN(t)
	first := startDurableServer(t, repoDir, headSHA, &durableE2E{DSN: dsn})
	second := startDurableServer(t, repoDir, headSHA, &durableE2E{DSN: dsn})
	Assert(t, first.storage.Store.InstanceID() != second.storage.Store.InstanceID(), "servers share an instance ID")
	return first, second
}

// holdInHook posts body to the server and returns once the command waits in
// its pre-workflow hook, still holding its execution claim. release lets it
// continue; wait blocks until the command finishes.
func (s durableServer) holdInHook(t *testing.T, body string) (release func(), wait func()) {
	t.Helper()
	entered, unblock := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	When(s.hooks.Run(Any[models.WorkflowHookCommandContext](), Any[string](), Any[string](), Any[string](), Any[string]())).
		Then(func([]Param) ReturnValues {
			enteredOnce.Do(func() { close(entered) })
			<-unblock
			return ReturnValues{"", "", nil}
		})
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.comment(t, body)
	}()
	select {
	case <-entered:
	case <-time.After(time.Minute):
		t.Fatal("command never reached its pre-workflow hook")
	}
	release = func() { releaseOnce.Do(func() { close(unblock) }) }
	wait = func() {
		t.Helper()
		select {
		case <-done:
		case <-time.After(time.Minute):
			t.Fatal("held command did not finish")
		}
	}
	t.Cleanup(func() { release(); <-done })
	return release, wait
}

func (s durableServer) jobCount(t *testing.T) int {
	t.Helper()
	all, err := s.storage.Store.Jobs()
	Ok(t, err)
	return len(all)
}

func testPull(t *testing.T, headSHA string) models.PullRequest {
	t.Helper()
	pull, _, _, err := (&events.EventParser{GithubUser: "github-user", GithubToken: "github-token"}).ParseGithubPull(logging.NewNoopLogger(t), GitHubPullRequestParsed(headSHA))
	Ok(t, err)
	return pull
}

// Two servers receive commands for one pull request. The second is refused
// while the first runs, then applies the plan the first server produced,
// restoring it from the shared artifact store.
func TestDurableStorageInstancesCompeteForPull(t *testing.T) {
	skipUnlessDurableE2E(t)
	dsn := sqlstoretest.PostgresDSN(t)
	repoDir, headSHA := initializeRepo(t, "simple")
	a := startDurableServer(t, repoDir, headSHA, &durableE2E{DSN: dsn})
	a.open(t, headSHA)
	lastReplyContains(t, a, "Plan: 3 to add")

	// Server B starts while server A runs a command, and leaves A's claim alone.
	repliesBefore := len(a.replies())
	release, wait := a.holdInHook(t, "atlantis plan")
	b := startDurableServer(t, repoDir, headSHA, &durableE2E{DSN: dsn})
	expired, err := b.storage.Store.ExpireClaims()
	Ok(t, err)
	Equals(t, 0, expired)
	jobsBefore := b.jobCount(t)
	b.comment(t, "atlantis apply")
	lastReplyContains(t, b, "already owned")
	b.comment(t, "atlantis plan")
	lastReplyContains(t, b, "already owned")
	Equals(t, jobsBefore, b.jobCount(t))
	release()
	wait()
	Equals(t, repliesBefore+1, len(a.replies()))
	lastReplyContains(t, a, "Plan: 3 to add")

	Equals(t, 0, len(pullDirs(t, b)))
	b.comment(t, "atlantis apply")
	lastReplyContains(t, b, "Apply complete!")
	locks, err := a.storage.Store.List()
	Ok(t, err)
	Equals(t, 1, len(locks))
}

// A webhook delivered to both servers runs once. The duplicate gets a 200
// response and no comments or jobs.
func TestDurableStorageDuplicateWebhookDelivery(t *testing.T) {
	repoDir, headSHA := initializeRepo(t, "simple")
	a, b := startSharedServers(t, repoDir, headSHA)
	a.open(t, headSHA)
	lastReplyContains(t, a, "Plan: 3 to add")

	delivery := func() *http.Request {
		request := GitHubCommentEvent(t, "atlantis plan")
		request.Header.Set("X-GitHub-Delivery", "6c1d3e2a-delivery")
		return request
	}
	w := httptest.NewRecorder()
	a.ctrl.Post(w, delivery())
	ResponseContains(t, w, 200, "Processing...")
	lastReplyContains(t, a, "Plan: 3 to add")
	jobsAfterFirst, repliesA, repliesB := a.jobCount(t), len(a.replies()), len(b.replies())

	// The provider redelivers to the other server, then to the first one.
	for _, server := range []durableServer{b, a} {
		w := httptest.NewRecorder()
		server.ctrl.Post(w, delivery())
		ResponseContains(t, w, 200, "Ignoring duplicate webhook delivery")
	}
	Equals(t, jobsAfterFirst, a.jobCount(t))
	Equals(t, repliesA, len(a.replies()))
	Equals(t, repliesB, len(b.replies()))
	b.vcs.VerifyWasCalled(Never()).ReactToComment(Any[logging.SimpleLogging](), Any[models.Repo](), Any[int](), Any[int64](), Any[string]())
}

// A cancel received by one server stops queued work of a command running on
// the other. The running command keeps its claim until it finishes.
func TestDurableStorageCancelFromOtherInstance(t *testing.T) {
	repoDir, headSHA := initializeRepo(t, "simple")
	a, b := startSharedServers(t, repoDir, headSHA)
	a.open(t, headSHA)
	lastReplyContains(t, a, "Plan: 3 to add")
	jobsBefore := a.jobCount(t)

	release, wait := a.holdInHook(t, "atlantis plan")
	b.comment(t, "atlantis cancel")
	lastReplyContains(t, b, "Running operations will continue")
	pull := testPull(t, headSHA)
	idle, err := b.storage.Store.PullIdle(pull)
	Ok(t, err)
	Assert(t, !idle, "cancel released the other server's claim")
	b.comment(t, "atlantis plan")
	lastReplyContains(t, b, "already owned")
	release()
	wait()
	Equals(t, jobsBefore, a.jobCount(t))
	idle, err = b.storage.Store.PullIdle(pull)
	Ok(t, err)
	Assert(t, idle, "cancelled command left its claim")

	b.comment(t, "atlantis plan")
	lastReplyContains(t, b, "Plan: 3 to add")
}

// A pull request closed on one server while a command runs on the other is
// cleaned up only after that command stops, by either server's maintenance.
// The reopened pull request then accepts commands.
func TestDurableStorageCloseWhileOtherInstanceRuns(t *testing.T) {
	repoDir, headSHA := initializeRepo(t, "simple")
	a, b := startSharedServers(t, repoDir, headSHA)
	a.open(t, headSHA)
	lastReplyContains(t, a, "Plan: 3 to add")

	release, wait := a.holdInHook(t, "atlantis plan")
	w := b.close(t)
	Assert(t, strings.Contains(w.Body.String(), "cleanup deferred"), "close did not defer cleanup: %s", w.Body.String())
	Equals(t, 1, len(pullDirs(t, a)))
	b.storage.maintain(t, logging.NewNoopLogger(t))
	locks, err := b.storage.Store.List()
	Ok(t, err)
	Equals(t, 1, len(locks))
	release()
	wait()

	b.storage.maintain(t, logging.NewNoopLogger(t))
	locks, err = b.storage.Store.List()
	Ok(t, err)
	Equals(t, 0, len(locks))
	pending, err := a.storage.Artifacts.Records.PendingPlans(testPull(t, headSHA).BaseRepo, testPull(t, headSHA).Num)
	Ok(t, err)
	Equals(t, 0, len(pending))

	request := GitHubPullRequestOpenedEvent(t, headSHA)
	body, err := io.ReadAll(request.Body)
	Ok(t, err)
	request.Body = io.NopCloser(bytes.NewReader(bytes.Replace(body, []byte(`"action": "opened"`), []byte(`"action": "reopened"`), 1)))
	repliesBefore := len(a.replies())
	response := httptest.NewRecorder()
	a.ctrl.Post(response, request)
	ResponseContains(t, response, 200, "Processing...")
	Equals(t, repliesBefore+1, len(a.replies()))
	lastReplyContains(t, a, "Plan: 3 to add")
}

// When a server stops refreshing its heartbeat, the other server's sweep marks
// its claim uncertain. Nothing takes the claim over: the other server recovers
// it only with the stopped-work attestation, after the work has stopped.
func TestDurableStorageInstanceFailureNeedsRecovery(t *testing.T) {
	repoDir, headSHA := initializeRepo(t, "simple")
	a, b := startSharedServers(t, repoDir, headSHA)
	a.open(t, headSHA)
	lastReplyContains(t, a, "Plan: 3 to add")

	release, wait := a.holdInHook(t, "atlantis plan")
	// Tests never run the heartbeat loop; age server A's claim past the expiry.
	sqlstoretest.AgeHeartbeats(t, a.storage.DSN, "server_instance_id = $1", a.storage.Store.InstanceID())
	expired, err := b.storage.Store.ExpireClaims()
	Ok(t, err)
	Equals(t, 1, expired)
	pull := testPull(t, headSHA)
	b.comment(t, "atlantis plan")
	lastReplyContains(t, b, "uncertain")
	b.comment(t, "atlantis unlock")
	lastReplyContains(t, b, "--confirm-stopped")

	// Server A resumes without a live claim; it cannot revive it.
	release()
	wait()
	refreshed, err := a.storage.Store.RefreshClaimHeartbeats()
	Ok(t, err)
	Equals(t, int64(0), refreshed)
	Assert(t, a.storage.Store.CheckClaim(pull) != nil, "expired claim authorized its server again")

	b.comment(t, "atlantis unlock --confirm-stopped")
	lastReplyContains(t, b, "unlocked and plans discarded")
	b.comment(t, "atlantis plan")
	lastReplyContains(t, b, "Plan: 3 to add")
}
