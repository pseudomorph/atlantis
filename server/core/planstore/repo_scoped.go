// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package planstore

import (
	"github.com/runatlantis/atlantis/server/core/artifacts"
	"github.com/runatlantis/atlantis/server/events/models"
)

// RepoScopedPlanStore is implemented by plan stores that locate plans by full
// repository identity, including the VCS host, instead of owner and name
// alone. The helpers below type-assert for it and fall back to the PlanStore
// methods with repo.Owner and repo.Name when a store does not implement it.
type RepoScopedPlanStore interface {
	DeleteForPullInRepo(repo models.Repo, pullNum int) error
	DeletePlanForProjectInRepo(repo models.Repo, pullNum int, workspace, repoRelDir, projectName string) error
}

// Inventory supplies authoritative pending plans when working directories are disposable.
type Inventory interface {
	PendingPlans(models.Repo, int) ([]models.Job, error)
}

var _ PlanStore = (*artifacts.Service)(nil)
var _ RepoScopedPlanStore = (*artifacts.Service)(nil)
var _ Inventory = (*artifacts.Service)(nil)

// DeleteForPullInRepo removes all stored plans for a pull request.
func DeleteForPullInRepo(s PlanStore, repo models.Repo, pullNum int) error {
	if scoped, ok := s.(RepoScopedPlanStore); ok {
		return scoped.DeleteForPullInRepo(repo, pullNum)
	}
	return s.DeleteForPull(repo.Owner, repo.Name, pullNum)
}

// DeletePlanForProjectInRepo removes one project's stored plan.
func DeletePlanForProjectInRepo(s PlanStore, repo models.Repo, pullNum int, workspace, repoRelDir, projectName string) error {
	if scoped, ok := s.(RepoScopedPlanStore); ok {
		return scoped.DeletePlanForProjectInRepo(repo, pullNum, workspace, repoRelDir, projectName)
	}
	return s.DeletePlanForProject(repo.Owner, repo.Name, pullNum, workspace, repoRelDir, projectName)
}
