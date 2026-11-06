// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package models_test

import (
	"testing"

	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestStorageIdentity(t *testing.T) {
	for _, tc := range []struct {
		provider                      models.VCSHostType
		name, clone, host, port, base string
	}{
		{models.Github, "org/repo", "https://token@GITHUB.com:443/org/repo.git", "github.com", "", ""},
		{models.Gitlab, "org/group/repo", "https://git.example:8443/gitlab/org/group/repo.git", "git.example", "8443", "/gitlab"},
		{models.Gitlab, "org/repo", "http://git.example:443/org/repo.git", "git.example", "443", ""},
		{models.BitbucketServer, "ORG/repo", "https://bitbucket.example/bitbucket/scm/org/repo.git", "bitbucket.example", "", "/bitbucket"},
		{models.AzureDevops, "org/project/repo", "https://dev.azure.com/org/project/_git/repo", "dev.azure.com", "", ""},
		{models.Gitlab, "org/repo", "https://[::1]:8443/gitlab/org/repo.git", "::1", "8443", "/gitlab"},
	} {
		t.Run(tc.clone, func(t *testing.T) {
			repo := models.Repo{FullName: tc.name, CloneURL: tc.clone, VCSHost: models.VCSHost{Type: tc.provider}}
			Equals(t, models.StorageInstance{Host: tc.host, Port: tc.port, BasePath: tc.base}, repo.StorageIdentity())
		})
	}
}
