// Copyright 2017 HootSuite Media Inc.
// SPDX-License-Identifier: Apache-2.0
// Modified hereafter by contributors to runatlantis/atlantis.
//
// Package locking handles locking projects when they have in-progress runs.
package locking

import (
	"errors"
	"regexp"
	"time"

	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/events/models"
)

// TryLockResponse results from an attempted lock.
type TryLockResponse struct {
	// LockAcquired is true if the lock was acquired from this call.
	LockAcquired bool
	// CurrLock is what project is currently holding the lock.
	CurrLock models.ProjectLock
	// LockKey is an identified by which to lookup and delete this lock.
	LockKey string
}

// Client is used to perform locking actions.
type Client struct {
	database db.Database
}

//go:generate go tool mockgen -package mocks -destination mocks/mock_locker.go . Locker

type Locker interface {
	TryLock(p models.Project, workspace string, pull models.PullRequest, user models.User) (TryLockResponse, error)
	Unlock(key string) (*models.ProjectLock, error)
	UnlockIfOwnedByPull(repo models.Repo, project models.Project, workspace string, pullNum int) (*models.ProjectLock, error)
	List() (map[string]models.ProjectLock, error)
	UnlockByPull(repo models.Repo, pullNum int) ([]models.ProjectLock, error)
	GetLock(key string) (*models.ProjectLock, error)
}

// NewClient returns a new locking client.
func NewClient(database db.Database) *Client {
	return &Client{
		database: database,
	}
}

// keyRegex matches and captures {repoFullName}/{path}/{workspace}/{projectName} where path can have multiple /'s in it.
var keyRegex = regexp.MustCompile(`^(.*?\/.*?)\/(.*)\/(.*)\/(.*)$`)

// TryLock attempts to acquire a lock to a project and workspace.
func (c *Client) TryLock(p models.Project, workspace string, pull models.PullRequest, user models.User) (TryLockResponse, error) {
	lock := models.ProjectLock{
		Workspace: workspace,
		Time:      time.Now().Local(),
		Project:   p,
		User:      user,
		Pull:      pull,
	}
	lockAcquired, currLock, err := c.database.TryLock(lock)
	if err != nil {
		return TryLockResponse{}, err
	}
	return TryLockResponse{lockAcquired, currLock, c.key(pull.BaseRepo, p, workspace)}, nil
}

// Unlock attempts to unlock a project and workspace. If successful,
// a pointer to the now deleted lock will be returned. Else, that
// pointer will be nil. An error will only be returned if there was
// an error deleting the lock (i.e. not if there was no lock).
func (c *Client) Unlock(key string) (*models.ProjectLock, error) {
	repo, project, workspace, err := c.decodeKey(key)
	if err != nil {
		return nil, err
	}
	if scoped, ok := c.database.(db.RepoScopedLocks); ok {
		return scoped.UnlockInRepo(repo, project, workspace)
	}
	return c.database.Unlock(project, workspace)
}

// UnlockIfOwnedByPull unlocks project and workspace only when it is still owned by pullNum.
func (c *Client) UnlockIfOwnedByPull(repo models.Repo, project models.Project, workspace string, pullNum int) (*models.ProjectLock, error) {
	if scoped, ok := c.database.(db.RepoScopedLocks); ok {
		return scoped.UnlockInRepoIfOwnedByPull(repo, project, workspace, pullNum)
	}
	return c.database.UnlockIfOwnedByPull(project, workspace, pullNum)
}

// List returns a map of all locks with their lock key as the map key.
// The lock key can be used in GetLock() and Unlock().
func (c *Client) List() (map[string]models.ProjectLock, error) {
	m := make(map[string]models.ProjectLock)
	locks, err := c.database.List()
	if err != nil {
		return m, err
	}
	for _, lock := range locks {
		m[c.key(lock.Pull.BaseRepo, lock.Project, lock.Workspace)] = lock
	}
	return m, nil
}

// UnlockByPull deletes all locks associated with that pull request.
func (c *Client) UnlockByPull(repo models.Repo, pullNum int) ([]models.ProjectLock, error) {
	if scoped, ok := c.database.(db.RepoScopedLocks); ok {
		return scoped.UnlockByPullInRepo(repo, pullNum)
	}
	return c.database.UnlockByPull(repo.FullName, pullNum)
}

// GetLock attempts to get the lock stored at key. If successful,
// a pointer to the lock will be returned. Else, the pointer will be nil.
// An error will only be returned if there was an error getting the lock
// (i.e. not if there was no lock).
func (c *Client) GetLock(key string) (*models.ProjectLock, error) {
	repo, project, workspace, err := c.decodeKey(key)
	if err != nil {
		return nil, err
	}

	if scoped, ok := c.database.(db.RepoScopedLocks); ok {
		return scoped.GetLockInRepo(repo, project, workspace)
	}
	return c.database.GetLock(project, workspace)
}

func (c *Client) key(repo models.Repo, p models.Project, workspace string) string {
	if codec, ok := c.database.(db.LockKeyCodec); ok {
		return codec.LockKey(repo, p, workspace)
	}
	return models.GenerateLockKey(p, workspace)
}

func (c *Client) decodeKey(key string) (models.Repo, models.Project, string, error) {
	if codec, ok := c.database.(db.LockKeyCodec); ok {
		return codec.ParseLockKey(key)
	}
	p, workspace, err := c.lockKeyToProjectWorkspace(key)
	return models.Repo{FullName: p.RepoFullName}, p, workspace, err
}

func IsCurrentLocking(key string) ([]string, error) {
	matches := keyRegex.FindStringSubmatch(key)
	if len(matches) != 5 {
		return []string{}, errors.New("invalid key format")
	}
	return matches, nil
}

func (c *Client) lockKeyToProjectWorkspace(key string) (models.Project, string, error) {
	matches, err := IsCurrentLocking(key)
	if err != nil {
		return models.Project{}, "", err
	}
	return models.Project{RepoFullName: matches[1], Path: matches[2], ProjectName: matches[4]}, matches[3], nil
}

type NoOpLocker struct{}

// NewNoOpLocker returns a new lno operation lockingclient.
func NewNoOpLocker() *NoOpLocker {
	return &NoOpLocker{}
}

// TryLock attempts to acquire a lock to a project and workspace.
func (c *NoOpLocker) TryLock(p models.Project, workspace string, _ models.PullRequest, _ models.User) (TryLockResponse, error) {
	return TryLockResponse{true, models.ProjectLock{}, c.key(p, workspace)}, nil
}

// Unlock attempts to unlock a project and workspace. If successful,
// a pointer to the now deleted lock will be returned. Else, that
// pointer will be nil. An error will only be returned if there was
// an error deleting the lock (i.e. not if there was no lock).
func (c *NoOpLocker) Unlock(_ string) (*models.ProjectLock, error) {
	return nil, nil
}

// UnlockIfOwnedByPull is a no-op for commands that do not use repository locks.
func (c *NoOpLocker) UnlockIfOwnedByPull(_ models.Repo, _ models.Project, _ string, _ int) (*models.ProjectLock, error) {
	return nil, nil
}

// List returns a map of all locks with their lock key as the map key.
// The lock key can be used in GetLock() and Unlock().
func (c *NoOpLocker) List() (map[string]models.ProjectLock, error) {
	m := make(map[string]models.ProjectLock)
	return m, nil
}

// UnlockByPull deletes all locks associated with that pull request.
func (c *NoOpLocker) UnlockByPull(_ models.Repo, _ int) ([]models.ProjectLock, error) {
	return []models.ProjectLock{}, nil
}

// GetLock attempts to get the lock stored at key. If successful,
// a pointer to the lock will be returned. Else, the pointer will be nil.
// An error will only be returned if there was an error getting the lock
// (i.e. not if there was no lock).
func (c *NoOpLocker) GetLock(_ string) (*models.ProjectLock, error) {
	return nil, nil
}

func (c *NoOpLocker) key(p models.Project, workspace string) string {
	return models.GenerateLockKey(p, workspace)
}
