// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"testing"
	"time"

	. "github.com/runatlantis/atlantis/testing"
	"gorm.io/gorm"
)

func TestDatabaseClock(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		s := conformanceStore(t)
		Ok(t, s.db.Transaction(func(tx *gorm.DB) error {
			before := time.Now()
			first, err := databaseNow(tx)
			after := time.Now()
			Ok(t, err)
			Equals(t, time.UTC, first.Location())
			Equals(t, 0, first.Nanosecond()%1000)
			Assert(t, !first.Before(before.Add(-time.Second)) && !first.After(after.Add(time.Second)), "database clock changed the current instant")
			time.Sleep(10 * time.Millisecond)
			second, err := databaseNow(tx)
			Ok(t, err)
			Assert(t, second.After(first), "database clock was frozen at transaction start")
			return nil
		}))
		tx := s.db.Begin()
		Ok(t, tx.Error)
		Ok(t, tx.Rollback().Error)
		_, err := databaseNow(tx)
		Assert(t, err != nil, "failed database clock silently used an application timestamp")
	})
}
