// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/runatlantis/atlantis/testing"
)

func TestRepositoryCredentialsNeverPersist(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenServer(dir)
	Ok(t, err)
	p := claimPull()
	p.BaseRepo.CloneURL = "https://test-user:distinct-secret-never-persist@git.example:8443/gitlab/team/infra.git"
	p.BaseRepo.SanitizedCloneURL = "https://test-user:<redacted>@git.example:8443/gitlab/team/infra.git"
	_, err = s.UpdatePullWithResults(p, nil)
	Ok(t, err)
	var repo repository
	Ok(t, s.db.First(&repo).Error)
	Equals(t, "https://git.example:8443/gitlab/team/infra.git", repo.CloneURL)
	var stored pull
	Ok(t, s.db.First(&stored).Error)
	loaded, err := loadPull(s.db, stored.ID)
	Ok(t, err)
	Equals(t, repo.CloneURL, loaded.BaseRepo.CloneURL)
	Equals(t, repo.CloneURL, loaded.BaseRepo.SanitizedCloneURL)
	Ok(t, s.Close())
	files, err := filepath.Glob(filepath.Join(dir, "atlantis.sqlite*"))
	Ok(t, err)
	for _, file := range files {
		data, err := os.ReadFile(file)
		Ok(t, err)
		Assert(t, !strings.Contains(string(data), "distinct-secret-never-persist"), "credential persisted in %s", file)
		Assert(t, !strings.Contains(string(data), "test-user"), "clone username persisted in %s", file)
	}
}
