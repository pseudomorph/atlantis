// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore_test

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/runatlantis/atlantis/server/core/locking"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func openStore(t *testing.T, dir string) *sqlstore.Store {
	t.Helper()
	s, err := sqlstore.OpenServer(dir)
	Ok(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func fixture() models.ProjectLock {
	repo := models.Repo{FullName: "team/infra", Owner: "team", Name: "infra", CloneURL: "https://user:secret@git.example/team/infra.git", VCSHost: models.VCSHost{Hostname: "git.example", Type: models.Gitlab}}
	return models.ProjectLock{Project: models.NewProject(repo.FullName, "env/prod", "network"), Workspace: "production",
		Pull: models.PullRequest{BaseRepo: repo, Num: 42, HeadCommit: "first", BaseBranch: "main", Author: "alice"}, User: models.User{Username: "alice", Teams: []string{"ops"}}, Time: time.Now().Truncate(time.Second)}
}

func result(name string) command.ProjectResult {
	return command.ProjectResult{RepoRelDir: "env/prod", ProjectName: name, Workspace: "production", PlanSuccess: &models.PlanSuccess{}}
}

func TestDurabilityAndOwnership(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	want := fixture()
	ok, _, err := s.TryLock(want)
	Ok(t, err)
	Assert(t, ok, "lock not acquired")
	_, err = s.UpdatePullWithResults(want.Pull, []command.ProjectResult{result("network"), result("app")})
	Ok(t, err)
	_, err = s.LockCommand(command.Apply, want.Time)
	Ok(t, err)
	Ok(t, s.Close())
	s = openStore(t, dir)
	Ok(t, s.Ping())
	got, err := s.GetLockInRepo(want.Pull.BaseRepo, want.Project, want.Workspace)
	Ok(t, err)
	Equals(t, publicLock(want), *got)
	global, err := s.CheckCommandLock(command.Apply)
	Ok(t, err)
	Equals(t, want.Time.Unix(), global.LockMetadata.UnixTime)
	_, err = s.LockCommand(command.Apply, time.Now())
	Assert(t, err != nil, "duplicate global lock accepted")
	Ok(t, s.UnlockCommand(command.Apply))
	Assert(t, s.UnlockCommand(command.Apply) != nil, "missing global lock accepted")
	deleted, err := s.UnlockInRepoIfOwnedByPull(want.Pull.BaseRepo, want.Project, want.Workspace, 43)
	Ok(t, err)
	Assert(t, deleted == nil, "another PR unlocked the project")
	Ok(t, s.UpdateProjectStatus(want.Pull, want.Workspace, want.Project.Path, models.DiscardedPlanStatus))
	status, err := s.GetPullStatus(want.Pull)
	Ok(t, err)
	Equals(t, models.DiscardedPlanStatus, status.Projects[0].Status)
	Assert(t, status.Projects[1].Status != models.DiscardedPlanStatus, "status update changed more than the first matching project")
	Ok(t, s.DeletePullStatus(want.Pull))
	got, err = s.GetLockInRepo(want.Pull.BaseRepo, want.Project, want.Workspace)
	Ok(t, err)
	Assert(t, got != nil, "deleting status removed a lock")
	deleted, err = s.UnlockInRepoIfOwnedByPull(want.Pull.BaseRepo, want.Project, want.Workspace, 42)
	Ok(t, err)
	Assert(t, deleted != nil, "owner could not unlock")
	want.Pull.Num = 43
	ok, _, err = s.TryLock(want)
	Ok(t, err)
	Assert(t, ok, "next owner could not acquire")
	deleted, err = s.UnlockInRepoIfOwnedByPull(want.Pull.BaseRepo, want.Project, want.Workspace, 42)
	Ok(t, err)
	Assert(t, deleted == nil, "stale owner removed successor's lock")
}

func TestInstanceIsolationAndLockKeys(t *testing.T) {
	s := openStore(t, t.TempDir())
	client := locking.NewClient(s)
	urls := []string{"https://git.example/team/infra.git", "https://other.example/team/infra.git", "https://git.example:8443/team/infra.git", "https://git.example/gitlab/team/infra.git"}
	keys := map[string]bool{}
	for _, u := range urls {
		want := fixture()
		want.Pull.BaseRepo.CloneURL = u
		lock, err := client.TryLock(want.Project, want.Workspace, want.Pull, want.User)
		Ok(t, err)
		Assert(t, lock.LockAcquired, "instance collision for %s", u)
		Assert(t, !keys[lock.LockKey], "duplicate key")
		keys[lock.LockKey] = true
		Assert(t, !strings.Contains(lock.LockKey, "secret"), "credentials in key")
		got, err := client.GetLock(lock.LockKey)
		Ok(t, err)
		Equals(t, u, got.Pull.BaseRepo.CloneURL)
		_, err = s.UpdatePullWithResults(want.Pull, []command.ProjectResult{result("network")})
		Ok(t, err)
	}
	locks, err := client.List()
	Ok(t, err)
	Equals(t, 4, len(locks))
	want := fixture()
	deleted, err := client.UnlockByPull(want.Pull.BaseRepo, want.Pull.Num)
	Ok(t, err)
	Equals(t, 1, len(deleted))
	for key := range locks {
		lock, _ := client.GetLock(key)
		if lock == nil {
			continue
		}
		_, err := client.Unlock(key)
		Ok(t, err)
	}
	locks, err = client.List()
	Ok(t, err)
	Equals(t, 0, len(locks))
	for _, u := range urls {
		want.Pull.BaseRepo.CloneURL = u
		status, err := s.GetPullStatus(want.Pull)
		Ok(t, err)
		Assert(t, status != nil, "another instance lost status")
	}
	_, err = client.GetLock("team/infra/env/prod/production/network")
	Assert(t, err != nil, "legacy key accepted by scoped store")
}

func TestConcurrentLocksAndStatus(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	var wg sync.WaitGroup
	winners := make(chan int, 16)
	errors := make(chan error, 32)
	for i := range 16 {
		wg.Go(func() {
			want := fixture()
			want.Pull.Num = i + 1
			ok, _, err := store.TryLock(want)
			if err != nil {
				errors <- err
			} else if ok {
				winners <- i
			}
		})
	}
	wg.Wait()
	close(winners)
	Equals(t, 1, len(winners))
	statusPull := fixture().Pull
	for i := range 16 {
		wg.Go(func() {
			_, err := store.UpdatePullWithResults(statusPull, []command.ProjectResult{result(fmt.Sprintf("project-%d", i))})
			if err != nil {
				errors <- err
			}
		})
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		Ok(t, err)
	}
	status, err := store.GetPullStatus(fixture().Pull)
	Ok(t, err)
	Equals(t, 16, len(status.Projects))
}

func TestCutoverRequiresFreshState(t *testing.T) {
	for _, name := range []string{"atlantis.db", "repos", "old-state"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			Ok(t, os.WriteFile(filepath.Join(dir, name), nil, 0600))
			_, err := sqlstore.OpenServer(dir)
			Assert(t, err != nil, "accepted legacy state")
			_, err = os.Stat(filepath.Join(dir, "atlantis.sqlite"))
			Assert(t, os.IsNotExist(err), "created database before rejecting cutover")
		})
	}
}

func TestSQLiteServerOwnership(t *testing.T) {
	dir := t.TempDir()
	first, err := sqlstore.OpenServer(dir)
	Ok(t, err)
	_, err = sqlstore.OpenServer(dir)
	Assert(t, err != nil, "second server acquired SQLite")
	Ok(t, first.Close())
	second, err := sqlstore.OpenServer(dir)
	Ok(t, err)
	Ok(t, second.Close())
}

func TestPlanHashPersistsAndTracksApplicablePlan(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	pull := fixture().Pull
	planned := result("network")
	planned.Command = command.Plan
	planned.PlanSuccess.PlanHash = strings.Repeat("a", 64)
	_, err := s.UpdatePullWithResults(pull, []command.ProjectResult{planned})
	Ok(t, err)
	Ok(t, s.Close())
	s = openStore(t, dir)
	check := func(want string) {
		t.Helper()
		status, err := s.GetPullStatus(pull)
		Ok(t, err)
		Equals(t, want, status.Projects[0].PlanHash)
	}
	check(planned.PlanSuccess.PlanHash)
	policy := planned
	policy.Command, policy.PlanSuccess = command.PolicyCheck, nil
	policy.PolicyCheckResults = &models.PolicyCheckResults{}
	_, err = s.UpdatePullWithResults(pull, []command.ProjectResult{policy})
	Ok(t, err)
	check(planned.PlanSuccess.PlanHash)
	apply := planned
	apply.Command, apply.PlanSuccess = command.Apply, nil
	apply.Error = errors.New("apply interrupted")
	_, err = s.UpdatePullWithResults(pull, []command.ProjectResult{apply})
	Ok(t, err)
	check(planned.PlanSuccess.PlanHash)
	apply.Error, apply.ApplySuccess = nil, "applied"
	_, err = s.UpdatePullWithResults(pull, []command.ProjectResult{apply})
	Ok(t, err)
	check("")
	_, err = s.UpdatePullWithResults(pull, []command.ProjectResult{planned})
	Ok(t, err)
	check(planned.PlanSuccess.PlanHash)
	pull.HeadCommit = "new-revision"
	_, err = s.UpdatePullWithResults(pull, []command.ProjectResult{policy})
	Ok(t, err)
	check("")
}

func publicLock(lock models.ProjectLock) models.ProjectLock {
	u, _ := url.Parse(lock.Pull.BaseRepo.CloneURL)
	u.User = nil
	lock.Pull.BaseRepo.CloneURL = u.String()
	lock.Pull.BaseRepo.SanitizedCloneURL = u.String()
	return lock
}
