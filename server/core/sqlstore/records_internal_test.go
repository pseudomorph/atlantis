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
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) {
		s, err := d.Open()
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
		if d.Name == "postgres" {
			var tables []string
			Ok(t, s.db.Raw("SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema()").Scan(&tables).Error)
			Assert(t, len(tables) > 0, "no tables found")
			for _, table := range tables {
				var matches int64
				Ok(t, s.db.Raw("SELECT count(*) FROM "+table+" AS r WHERE r::text LIKE '%distinct-secret-never-persist%' OR r::text LIKE '%test-user%'").Scan(&matches).Error)
				Equals(t, int64(0), matches)
			}
			Ok(t, s.Close())
			return
		}
		Ok(t, s.Close())
		files, err := filepath.Glob(filepath.Join(d.DataDir, "atlantis.sqlite*"))
		Ok(t, err)
		for _, file := range files {
			data, err := os.ReadFile(file)
			Ok(t, err)
			Assert(t, !strings.Contains(string(data), "distinct-secret-never-persist"), "credential persisted in %s", file)
			Assert(t, !strings.Contains(string(data), "test-user"), "clone username persisted in %s", file)
		}
	})
}
