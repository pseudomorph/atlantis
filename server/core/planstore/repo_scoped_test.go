// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package planstore

import (
	"testing"

	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

type legacyRecorder struct{ calls []string }

func (r *legacyRecorder) Save(command.ProjectContext, string) error   { return nil }
func (r *legacyRecorder) Load(command.ProjectContext, string) error   { return nil }
func (r *legacyRecorder) Remove(command.ProjectContext, string) error { return nil }
func (r *legacyRecorder) ListWorkspaces(owner, repo string, _ int) ([]string, error) {
	r.calls = append(r.calls, "list "+owner+" "+repo)
	return nil, nil
}
func (r *legacyRecorder) RestorePlans(_, owner, repo string, _ int) error {
	r.calls = append(r.calls, "restore "+owner+" "+repo)
	return nil
}
func (r *legacyRecorder) DeleteForPull(owner, repo string, _ int) error {
	r.calls = append(r.calls, "delete "+owner+" "+repo)
	return nil
}
func (r *legacyRecorder) DeletePlanForProject(owner, repo string, _ int, _, _, _ string) error {
	r.calls = append(r.calls, "delete-project "+owner+" "+repo)
	return nil
}

type scopedRecorder struct {
	legacyRecorder
	repos []models.Repo
}

func (r *scopedRecorder) ListWorkspacesInRepo(repo models.Repo, _ int) ([]string, error) {
	r.repos = append(r.repos, repo)
	return nil, nil
}
func (r *scopedRecorder) RestorePlansInRepo(_ string, repo models.Repo, _ int) error {
	r.repos = append(r.repos, repo)
	return nil
}
func (r *scopedRecorder) DeleteForPullInRepo(repo models.Repo, _ int) error {
	r.repos = append(r.repos, repo)
	return nil
}
func (r *scopedRecorder) DeletePlanForProjectInRepo(repo models.Repo, _ int, _, _, _ string) error {
	r.repos = append(r.repos, repo)
	return nil
}

func callAll(t *testing.T, s PlanStore, repo models.Repo) {
	_, err := ListWorkspacesInRepo(s, repo, 1)
	Ok(t, err)
	Ok(t, RestorePlansInRepo(s, "dir", repo, 1))
	Ok(t, DeleteForPullInRepo(s, repo, 1))
	Ok(t, DeletePlanForProjectInRepo(s, repo, 1, "default", ".", ""))
}

// Legacy stores keep receiving owner and name exactly as before.
func TestRepoScopedHelpersFallBackToLegacyMethods(t *testing.T) {
	store := &legacyRecorder{}
	callAll(t, store, models.Repo{FullName: "group/sub/repo", Owner: "group/sub", Name: "repo"})
	Equals(t, []string{"list group/sub repo", "restore group/sub repo", "delete group/sub repo", "delete-project group/sub repo"}, store.calls)
}

func TestRepoScopedHelpersPreferRepositoryIdentity(t *testing.T) {
	store := &scopedRecorder{}
	repo := models.Repo{FullName: "org/repo", Owner: "org", Name: "repo", VCSHost: models.VCSHost{Hostname: "git.example", Type: models.Gitlab}}
	callAll(t, store, repo)
	Equals(t, 0, len(store.calls))
	Equals(t, []models.Repo{repo, repo, repo, repo}, store.repos)
}
