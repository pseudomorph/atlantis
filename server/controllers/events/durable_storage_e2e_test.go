// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events_test

import (
	"bytes"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/petergtz/pegomock/v4"
	"github.com/runatlantis/atlantis/server"
	events_controllers "github.com/runatlantis/atlantis/server/controllers/events"
	"github.com/runatlantis/atlantis/server/core/artifacts"
	"github.com/runatlantis/atlantis/server/core/artifacts/artifactstest"
	runtimemocks "github.com/runatlantis/atlantis/server/core/runtime/mocks"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/core/sqlstore/sqlstoretest"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/models"
	vcsmocks "github.com/runatlantis/atlantis/server/events/vcs/mocks"
	"github.com/runatlantis/atlantis/server/jobs"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

// durableE2E holds one server's durable-storage runtime. Reusing DataDir in a
// second setup restarts the server against the same database and artifacts.
// Setting DSN instead starts another server, with its own data directory,
// sharing that PostgreSQL database.
type durableE2E struct {
	DataDir   string
	DSN       string
	Store     *sqlstore.Store
	Artifacts *artifacts.Service
	Jobs      *jobs.Service
	Execution *events.ExecutionCoordinator
	Cleaner   *events.PullClosedExecutor
	// Objects, when set, holds plan snapshots instead of the database, as the
	// filesystem and S3 artifact backends do.
	Objects artifacts.Store
	closed  bool
	// restarted is set when setup reuses DataDir.
	restarted bool
}

// durableDatabaseEnv selects the database for the durable e2e tests: unset for
// SQLite, or "postgres" for the database in sqlstoretest.PostgresDSNEnv.
const durableDatabaseEnv = "ATLANTIS_TEST_DURABLE_DATABASE"

// durableDSNs maps a data directory to its PostgreSQL schema, so a restart
// against the same data directory reopens the same database.
var durableDSNs sync.Map

// durableDSN returns the PostgreSQL connection string for dataDir, creating a
// schema on first use, or "" when the tests run on SQLite.
func durableDSN(t *testing.T, dataDir string) string {
	t.Helper()
	if os.Getenv(durableDatabaseEnv) != "postgres" {
		return ""
	}
	if dsn := os.Getenv("ATLANTIS_DURABLE_DSN"); dsn != "" {
		return dsn
	}
	if dsn, ok := durableDSNs.Load(dataDir); ok {
		return dsn.(string)
	}
	dsn := sqlstoretest.PostgresDSN(t)
	durableDSNs.Store(dataDir, dsn)
	return dsn
}

func (d *durableE2E) open(t *testing.T) {
	t.Helper()
	var store *sqlstore.Store
	var err error
	dsn := d.DSN
	if dsn == "" {
		dsn = durableDSN(t, d.DataDir)
	}
	if dsn != "" {
		d.DSN = dsn
		store, err = sqlstore.OpenPostgresServer(dsn, d.DataDir)
	} else {
		store, err = sqlstore.OpenServer(d.DataDir)
	}
	Ok(t, err)
	// Startup marks work left by a previous process uncertain, as the server does.
	Ok(t, store.MarkPreviousExecutionsUncertain())
	if dsn != "" && d.restarted {
		// On PostgreSQL the previous process's claims stay active until their
		// heartbeat expires. Restart after the expiry, once the sweep has run.
		sqlstoretest.AgeHeartbeats(t, dsn, "server_instance_id <> $1", store.InstanceID())
		_, err := store.ExpireClaims()
		Ok(t, err)
	}
	var blobs artifacts.SnapshotBackend = sqlstore.BlobStore{Store: store}
	if d.Objects != nil {
		blobs = artifacts.ObjectSnapshots{Store: d.Objects}
	}
	d.Store = store
	d.Artifacts = &artifacts.Service{Records: store, Blobs: blobs}
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
	// hooks is this server's pre-workflow hook runner. Setup replaces the
	// package-level mock, so a test running two servers stubs this one.
	hooks *runtimemocks.MockPreWorkflowHookRunner
}

func startDurableServer(t *testing.T, repoDir, headSHA string, storage *durableE2E) durableServer {
	t.Helper()
	userConfig.EnablePolicyChecksFlag = false
	ctrl, vcsClient, githubGetter, workspace := setupE2E(t, "simple", setupOption{durable: storage})
	workspace.TestingOverrideHeadCloneURL = fmt.Sprintf("file://%s", repoDir)
	When(githubGetter.GetPullRequest(Any[logging.SimpleLogging](), Any[models.Repo](), Any[int]())).ThenReturn(GitHubPullRequestParsed(headSHA), nil)
	When(vcsClient.GetModifiedFiles(Any[logging.SimpleLogging](), Any[models.Repo](), Any[models.PullRequest]())).ThenReturn([]string{"main.tf"}, nil)
	return durableServer{ctrl: ctrl, vcs: vcsClient, workspace: workspace, storage: storage, hooks: mockPreWorkflowHookRunner}
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

// Locks, pull status and the plan snapshot live in durable storage, so a
// restarted server applies the plan that the first server recorded.
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

// With S3 artifacts, a restarted server that lost its working directory
// restores the plan snapshot from the bucket and applies it.
func TestDurableStorageS3PlanSurvivesRestartAndLostWorkingDirectory(t *testing.T) {
	skipUnlessDurableE2E(t)
	repoDir, headSHA := initializeRepo(t, "simple")
	fake := artifactstest.New("artifacts")
	bucket := func() artifacts.Store {
		store, err := artifacts.NewS3(fake, fake.Bucket, "atlantis", filepath.Join(t.TempDir(), "uploads"))
		Ok(t, err)
		return store
	}
	storage := &durableE2E{Objects: bucket()}
	first := startDurableServer(t, repoDir, headSHA, storage)
	first.open(t, headSHA)
	lastReplyContains(t, first, "Plan: 3 to add")
	keys := fake.Keys()
	Equals(t, 1, len(keys))
	Assert(t, strings.HasPrefix(keys[0], "atlantis/snapshots/v1/"), "plan snapshot stored outside the prefix: %v", keys)
	Ok(t, os.RemoveAll(pullWorkingDir(first)))
	first.storage.stop(t)

	restarted := startDurableServer(t, repoDir, headSHA, &durableE2E{DataDir: storage.DataDir, Objects: bucket()})
	restarted.comment(t, "atlantis apply")
	lastReplyContains(t, restarted, "Apply complete!")
}

func TestDurableStorageRestoresPlanIntoLostWorkingDirectory(t *testing.T) {
	skipUnlessDurableE2E(t)
	repoDir, headSHA := initializeRepo(t, "simple")

	s := startDurableServer(t, repoDir, headSHA, &durableE2E{})
	s.open(t, headSHA)
	lastReplyContains(t, s, "Plan: 3 to add")
	Ok(t, os.RemoveAll(pullWorkingDir(s)))
	s.comment(t, "atlantis apply")
	lastReplyContains(t, s, "Apply complete!")
}

func TestDurableStorageRestoresBackendDataAndWorkspace(t *testing.T) {
	for _, workspace := range []string{"default", "staging"} {
		t.Run(workspace, func(t *testing.T) {
			skipUnlessDurableE2E(t)
			repoDir, headSHA := initializeRepo(t, "simple")
			stateDir := filepath.ToSlash(t.TempDir())
			backend := fmt.Sprintf("terraform {\n backend \"local\" {\n path = %q\n workspace_dir = %q\n }\n}\n", stateDir+"/state.tfstate", stateDir+"/workspaces")
			Ok(t, os.WriteFile(filepath.Join(repoDir, "backend.tf"), []byte(backend), 0600))
			workflow := fmt.Sprintf(`version: 3
projects:
- dir: .
  workspace: %s
  workflow: restore
workflows:
  restore:
    plan:
      steps:
      - env:
          name: TF_DATA_DIR
          value: .tfdata
      - init
      - plan
    apply:
      steps:
      - env:
          name: TF_DATA_DIR
          value: .tfdata
      - apply
`, workspace)
			Ok(t, os.WriteFile(filepath.Join(repoDir, "atlantis.yaml"), []byte(workflow), 0600))
			runCmd(t, repoDir, "git", "add", "backend.tf", "atlantis.yaml")
			runCmd(t, repoDir, "git", "commit", "-m", "explicit backend and data directory")
			headSHA = strings.TrimSpace(runCmd(t, repoDir, "git", "rev-parse", "HEAD"))
			s := startDurableServer(t, repoDir, headSHA, &durableE2E{})
			s.open(t, headSHA)
			lastReplyContains(t, s, "Plan: 3 to add")
			all, err := s.storage.Store.Jobs()
			Ok(t, err)
			var plans []models.Job
			for _, job := range all {
				if job.Command == "plan" {
					plans = append(plans, job)
				}
			}

			Equals(t, 1, len(plans))
			records, err := s.storage.Store.Snapshot(plans[0].ID)
			Ok(t, err)
			backendSaved, workspaceSaved := false, false
			for _, a := range records.Manifest.Files {
				backendSaved = backendSaved || a.Kind == "backend"
				workspaceSaved = workspaceSaved || a.Kind == "workspace"
			}
			Assert(t, backendSaved && workspaceSaved, "initialization metadata was not captured")
			Ok(t, os.RemoveAll(pullWorkingDir(s)))
			s.comment(t, "atlantis apply")
			lastReplyContains(t, s, "Apply complete!")
		})
	}
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

func TestDurableStorageRestoresProviderFreeBackend(t *testing.T) {
	skipUnlessDurableE2E(t)
	repoDir, _ := initializeRepo(t, "simple")
	statePath := filepath.ToSlash(filepath.Join(t.TempDir(), "state.tfstate"))
	configuration := fmt.Sprintf("terraform {\n backend \"local\" {\n path = %q\n }\n}\noutput \"restored\" {\n value = \"provider-free\"\n}\n", statePath)
	Ok(t, os.WriteFile(filepath.Join(repoDir, "main.tf"), []byte(configuration), 0600))
	Ok(t, os.Remove(filepath.Join(repoDir, "versions.tf")))
	runCmd(t, repoDir, "git", "add", "main.tf", "versions.tf")
	runCmd(t, repoDir, "git", "commit", "-m", "provider-free backend")
	headSHA := strings.TrimSpace(runCmd(t, repoDir, "git", "rev-parse", "HEAD"))
	s := startDurableServer(t, repoDir, headSHA, &durableE2E{})
	s.open(t, headSHA)
	lastReplyContains(t, s, "Changes to Outputs")
	all, err := s.storage.Store.Jobs()
	Ok(t, err)
	found := false
	for _, job := range all {
		if job.Command != "plan" {
			continue
		}
		snapshot, err := s.storage.Store.Snapshot(job.ID)
		Ok(t, err)
		Assert(t, snapshot != nil, "provider-free plan snapshot missing")
		_, hasLock := snapshot.Manifest.File("lockfile")
		_, hasBackend := snapshot.Manifest.File("backend")
		Assert(t, !hasLock && hasBackend && snapshot.Manifest.DataDirectory == ".terraform", "provider-free initialization metadata missing")
		found = true
	}
	Assert(t, found, "no plan job recorded")
	Ok(t, os.RemoveAll(pullWorkingDir(s)))
	s.comment(t, "atlantis apply")
	lastReplyContains(t, s, "Apply complete!")
}

func TestDurableStorageRestoresPlanOnlyBackendArguments(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "cached configuration", true: "changed override rejected"}[changed], func(t *testing.T) {
			skipUnlessDurableE2E(t)
			repoDir, _ := initializeRepo(t, "simple")
			statePath := filepath.ToSlash(filepath.Join(t.TempDir(), "planned.tfstate"))
			configuration := "terraform {\n backend \"local\" {}\n}\noutput \"restored\" { value = \"partial-backend\" }\n"
			Ok(t, os.WriteFile(filepath.Join(repoDir, "main.tf"), []byte(configuration), 0600))
			Ok(t, os.Remove(filepath.Join(repoDir, "versions.tf")))
			applySteps := "      - apply"
			changedPath := filepath.ToSlash(filepath.Join(t.TempDir(), "changed.tfstate"))
			if changed {
				applySteps = fmt.Sprintf("      - env:\n          name: TF_CLI_ARGS_init\n          value: '-backend-config=path=%s'\n      - apply", changedPath)
			}
			workflow := fmt.Sprintf(`version: 3
projects:
- dir: .
  workflow: restore
workflows:
  restore:
    plan:
      steps:
      - env:
          name: TF_CLI_ARGS_init
          value: '-backend-config=path=%s'
      - init
      - plan
    apply:
      steps:
%s
`, statePath, applySteps)
			Ok(t, os.WriteFile(filepath.Join(repoDir, "atlantis.yaml"), []byte(workflow), 0600))
			runCmd(t, repoDir, "git", "add", "main.tf", "versions.tf", "atlantis.yaml")
			runCmd(t, repoDir, "git", "commit", "-m", "partial backend configuration")
			headSHA := strings.TrimSpace(runCmd(t, repoDir, "git", "rev-parse", "HEAD"))
			s := startDurableServer(t, repoDir, headSHA, &durableE2E{})
			s.open(t, headSHA)
			lastReplyContains(t, s, "Changes to Outputs")
			Ok(t, os.RemoveAll(pullWorkingDir(s)))
			s.comment(t, "atlantis apply")
			if changed {
				lastReplyContains(t, s, "Backend configuration changed")
				_, err := os.Stat(changedPath)
				Assert(t, os.IsNotExist(err), "changed backend received state")
			} else {
				lastReplyContains(t, s, "Apply complete!")
				_, err := os.Stat(statePath)
				Ok(t, err)
			}
		})
	}
}
