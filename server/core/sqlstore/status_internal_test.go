// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"testing"

	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestRevisionAndTargetChangesReplaceProjects(t *testing.T) {
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) {
		approved := []models.PolicySetStatus{{PolicySetName: "security", Passed: true}}
		p := models.PullRequest{HeadCommit: "first", BaseBranch: "main"}
		prior := &models.PullStatus{Pull: p, Projects: []models.ProjectStatus{
			{RepoRelDir: ".", ProjectName: "keep", Workspace: "default", PolicyStatus: approved},
			{RepoRelDir: "removed", Workspace: "default"},
		}}
		results := []command.ProjectResult{{RepoRelDir: ".", ProjectName: "keep", Workspace: "default"}}
		for _, changed := range []models.PullRequest{{HeadCommit: "second", BaseBranch: "main"}, {HeadCommit: "first", BaseBranch: "release"}} {
			got := mergeStatus(prior, changed, results)
			Equals(t, 1, len(got.Projects))
			Equals(t, approved, got.Projects[0].PolicyStatus)
		}
		got := mergeStatus(prior, p, results)
		Equals(t, 2, len(got.Projects))
	})
}

func TestStatusRelationshipsAndCascade(t *testing.T) {
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) {
		s := d.Store(t)
		p := claimPull()
		p.Execution = &models.Execution{Owner: "first"}
		Ok(t, s.AcquireClaim(p, p.ExecutionOwner()))
		result := command.ProjectResult{RepoRelDir: ".", Workspace: "default"}
		_, err := s.UpdatePullWithResults(p, []command.ProjectResult{result})
		Ok(t, err)
		var first projectStatus
		Ok(t, s.db.Where("pull_id IN (?)", pullIDs(s.db, p.BaseRepo, p.Num)).Take(&first).Error)

		other := p
		other.BaseRepo.FullName = "team/other"
		other.Execution = &models.Execution{Owner: "other-repository"}
		Ok(t, s.AcquireClaim(other, other.ExecutionOwner()))
		_, err = s.UpdatePullWithResults(other, []command.ProjectResult{result})
		Ok(t, err)
		var second projectStatus
		Ok(t, s.db.Where("pull_id IN (?)", pullIDs(s.db, other.BaseRepo, other.Num)).Take(&second).Error)
		foreignKeyViolation(t, s.db.Exec("UPDATE project_statuses SET repository_id = ? WHERE pull_id = ?", second.RepositoryID, first.PullID).Error)
		foreignKeyViolation(t, s.db.Exec("UPDATE project_statuses SET repository_id = ?, project_id = ? WHERE pull_id = ?", second.RepositoryID, second.ProjectID, first.PullID).Error)
		foreignKeyViolation(t, s.db.Exec("UPDATE project_statuses SET project_id = ? WHERE pull_id = ?", second.ProjectID, first.PullID).Error)

		withoutStatus := p
		withoutStatus.Num++
		withoutStatus.Execution = &models.Execution{Owner: "no-status"}
		Ok(t, s.AcquireClaim(withoutStatus, withoutStatus.ExecutionOwner()))
		var stored pull
		Ok(t, s.db.Where("id IN (?)", pullIDs(s.db, withoutStatus.BaseRepo, withoutStatus.Num)).Take(&stored).Error)
		foreignKeyViolation(t, s.db.Exec("UPDATE project_statuses SET pull_id = ? WHERE pull_id = ?", stored.ID, first.PullID).Error)

		Ok(t, s.DeletePullStatus(p))
		var count int64
		Ok(t, s.db.Model(&projectStatus{}).Where("pull_id = ?", first.PullID).Count(&count).Error)
		Equals(t, int64(0), count)
		Ok(t, s.db.Model(&pull{}).Where("id = ?", first.PullID).Count(&count).Error)
		Equals(t, int64(1), count)
		Ok(t, s.db.Model(&projectStatus{}).Where("pull_id = ?", second.PullID).Count(&count).Error)
		Equals(t, int64(1), count)
	})
}
