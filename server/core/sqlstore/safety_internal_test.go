// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestRepositoryCredentialsNeverPersist(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenServer(dir)
	Ok(t, err)
	p := claimPull()
	p.BaseRepo.CloneURL = "https://test-user:distinct-secret-never-persist@git.example:8443/gitlab/team/infra.git"
	p.BaseRepo.SanitizedCloneURL = "https://test-user:<redacted>@git.example:8443/gitlab/team/infra.git"
	Ok(t, s.AcquireClaim(p, "owner"))
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

func TestSyntheticCollisionDoesNotOverwriteHistory(t *testing.T) {
	for _, state := range []string{"active", "uncertain", "released"} {
		t.Run(state, func(t *testing.T) {
			s := conformanceStore(t)
			p := claimPull()
			p.Num = -17
			p.Execution = &models.Execution{Owner: "first"}
			Ok(t, s.AcquireClaim(p, "first"))
			if state == "active" {
				_, err := s.UpdatePullWithResults(p, nil)
				Ok(t, err)
			}
			if state == "uncertain" {
				Ok(t, s.MarkUncertain(p))
			}
			if state == "released" {
				Ok(t, s.ReleaseClaim(p))
			}
			other := p
			other.HeadCommit = "replacement"
			other.Execution = &models.Execution{Owner: "next"}
			Assert(t, errors.Is(s.AcquireClaim(other, "next"), db.ErrSyntheticPullCollision), "historical API identity was reused")
			var stored pull
			Ok(t, s.db.First(&stored).Error)
			Equals(t, p.HeadCommit, stored.HeadCommit)
			var count int64
			Ok(t, s.db.Table("pulls").Count(&count).Error)
			Equals(t, int64(1), count)
		})
	}
}
