// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"testing"
	"time"

	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestPortableSchemaConformance(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		t.Run("lock listing follows creation time", func(t *testing.T) {
			s := conformanceStore(t)
			p := claimPull()
			Ok(t, s.AcquireClaim(p, "owner"))
			p.Execution = &models.Execution{Owner: "owner"}
			at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
			for i, directory := range []string{"second", "first"} {
				lock := models.ProjectLock{Pull: p, Project: models.NewProject(p.BaseRepo.FullName, directory, ""), Workspace: "default", Time: at.Add(-time.Duration(i) * time.Hour)}
				acquired, _, err := s.TryLock(lock)
				Ok(t, err)
				Assert(t, acquired, "project lock unavailable")
			}
			locks, err := s.List()
			Ok(t, err)
			Equals(t, 2, len(locks))
			Equals(t, "first", locks[0].Project.Path)
			Equals(t, "second", locks[1].Project.Path)
		})
	})
}
