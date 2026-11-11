// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"fmt"
	"net/url"
	"time"

	"github.com/runatlantis/atlantis/server/core/db"

	"github.com/google/uuid"

	"github.com/runatlantis/atlantis/server/events/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type vcsInstance struct {
	ID                   string
	Host, Port, BasePath string
	Provider             models.VCSHostType
}

type repository struct {
	ID, InstanceID                            string
	FullName, Owner, Name, CloneURL, Hostname string
}

type pull struct {
	ClosureGeneration                               int64
	ID, RepositoryID                                string
	Number                                          int
	HeadCommit, URL, HeadBranch, BaseBranch, Author string
	State                                           models.PullRequestState
}

type project struct {
	ID, RepositoryID           string
	Directory, Name, Workspace string
}

type job struct {
	MutationStarted                 bool
	Kind                            models.JobKind
	ManagedPlan                     bool
	ID                              string `gorm:"primaryKey"`
	Owner                           string
	RepositoryID, PullID, ProjectID string
	Command, SubCommand             string
	Arguments                       []string `gorm:"serializer:json"`
	Steps, Issuer                   string
	IssuerTeams                     []string `gorm:"serializer:json"`
	Trigger, Revision               string
	PlanJobID                       *string
	State, Error                    string
	Private                         bool
	StartedAt                       time.Time
	FinishedAt                      *time.Time
}

type pendingPlan struct {
	ProjectID string `gorm:"primaryKey;autoIncrement:false"`
	PullID    string `gorm:"primaryKey;autoIncrement:false"`
	JobID     string
	Ready     bool
}

func repoIDs(tx *gorm.DB, repo models.Repo) *gorm.DB {
	i := repo.StorageIdentity()
	instances := tx.Model(&vcsInstance{}).Select("id").Where("host = ? AND port = ? AND base_path = ? AND provider = ?", i.Host, i.Port, i.BasePath, repo.VCSHost.Type)
	return tx.Model(&repository{}).Select("id").Where("instance_id IN (?) AND full_name = ?", instances, repo.FullName)
}

func pullIDs(tx *gorm.DB, repo models.Repo, number int) *gorm.DB {
	return tx.Model(&pull{}).Select("id").Where("repository_id IN (?) AND number = ?", repoIDs(tx, repo), number)
}

func projectIDs(tx *gorm.DB, repo models.Repo, p models.Project, workspace string) *gorm.DB {
	return tx.Model(&project{}).Select("id").Where("repository_id IN (?) AND directory = ? AND name = ? AND workspace = ?", repoIDs(tx, repo), p.Path, p.ProjectName, workspace)
}

func savePull(tx *gorm.DB, p models.PullRequest) (pull, error) {
	i := p.BaseRepo.StorageIdentity()
	instance := vcsInstance{ID: uuid.NewString(), Host: i.Host, Port: i.Port, BasePath: i.BasePath, Provider: p.BaseRepo.VCSHost.Type}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&instance).Error; err != nil {
		return pull{}, err
	}
	instance.ID = ""
	if err := tx.Where("host = ? AND port = ? AND base_path = ?", i.Host, i.Port, i.BasePath).Take(&instance).Error; err != nil {
		return pull{}, err
	}
	if instance.Provider != p.BaseRepo.VCSHost.Type {
		return pull{}, fmt.Errorf("VCS instance %q already belongs to another provider", i.Host)
	}
	cloneURL, err := credentialFreeCloneURL(p.BaseRepo.CloneURL)
	if err != nil {
		return pull{}, err
	}
	r := repository{ID: uuid.NewString(), InstanceID: instance.ID, FullName: p.BaseRepo.FullName, Owner: p.BaseRepo.Owner, Name: p.BaseRepo.Name,
		CloneURL: cloneURL, Hostname: p.BaseRepo.VCSHost.Hostname}
	if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "instance_id"}, {Name: "full_name"}}, DoUpdates: clause.AssignmentColumns([]string{"owner", "name", "clone_url", "hostname"})}).Create(&r).Error; err != nil {
		return pull{}, err
	}
	r.ID = ""
	if err := tx.Where("instance_id = ? AND full_name = ?", instance.ID, r.FullName).Take(&r).Error; err != nil {
		return pull{}, err
	}
	row := pull{ID: uuid.NewString(), RepositoryID: r.ID, Number: p.Num, HeadCommit: p.HeadCommit, URL: p.URL, HeadBranch: p.HeadBranch, BaseBranch: p.BaseBranch, Author: p.Author, State: p.State}
	conflict := clause.OnConflict{Columns: []clause.Column{{Name: "repository_id"}, {Name: "number"}}, DoUpdates: clause.AssignmentColumns([]string{"head_commit", "url", "head_branch", "base_branch", "author", "state"})}
	if p.Num < 0 {
		conflict = clause.OnConflict{DoNothing: true}
	}
	result := tx.Clauses(conflict).Create(&row)
	if result.Error != nil {
		return pull{}, result.Error
	}
	if p.Num < 0 && result.RowsAffected == 0 {
		if p.ExecutionOwner() == "" {
			return pull{}, db.ErrSyntheticPullCollision
		}
		var bound int64
		if err := tx.Table("execution_claims").Where("owner = ? AND pull_id IN (?)", p.ExecutionOwner(), tx.Model(&pull{}).Select("id").Where("repository_id = ? AND number = ?", r.ID, p.Num)).Count(&bound).Error; err != nil {
			return pull{}, err
		}
		if bound != 1 {
			return pull{}, db.ErrSyntheticPullCollision
		}
	}
	row.ID = ""
	err = tx.Where("repository_id = ? AND number = ?", r.ID, p.Num).Take(&row).Error
	return row, err
}

func saveProject(tx *gorm.DB, repoID string, directory, name, workspace string) (project, error) {
	row := project{ID: uuid.NewString(), RepositoryID: repoID, Directory: directory, Name: name, Workspace: workspace}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return row, err
	}
	row.ID = ""
	err := tx.Where("repository_id = ? AND directory = ? AND name = ? AND workspace = ?", repoID, directory, name, workspace).Take(&row).Error
	return row, err
}

func loadPull(tx *gorm.DB, id string) (models.PullRequest, error) {
	var p pull
	var r repository
	var i vcsInstance
	if err := tx.Where("id = ?", id).Take(&p).Error; err != nil {
		return models.PullRequest{}, err
	}
	if err := tx.Where("id = ?", p.RepositoryID).Take(&r).Error; err != nil {
		return models.PullRequest{}, err
	}
	if err := tx.Where("id = ?", r.InstanceID).Take(&i).Error; err != nil {
		return models.PullRequest{}, err
	}
	return models.PullRequest{ClosureGeneration: p.ClosureGeneration, Num: p.Number, HeadCommit: p.HeadCommit, URL: p.URL, HeadBranch: p.HeadBranch, BaseBranch: p.BaseBranch, Author: p.Author, State: p.State,
		BaseRepo: models.Repo{FullName: r.FullName, Owner: r.Owner, Name: r.Name, CloneURL: r.CloneURL, SanitizedCloneURL: r.CloneURL,
			VCSHost: models.VCSHost{Hostname: r.Hostname, Type: i.Provider}}}, nil
}

// Persist repository locations, never credentials that can expire or rotate.
func credentialFreeCloneURL(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid repository clone URL")
	}
	u.User = nil
	return u.String(), nil
}
