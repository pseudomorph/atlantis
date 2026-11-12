// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/runatlantis/atlantis/testing"
)

func TestWebhookDeliveryIsRecordedOnce(t *testing.T) {
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) {
		s := d.Store(t)
		first, err := s.RecordDelivery("github", "delivery-1")
		Ok(t, err)
		Assert(t, first, "new delivery reported as duplicate")
		again, err := s.RecordDelivery("github", "delivery-1")
		Ok(t, err)
		Assert(t, !again, "duplicate delivery reported as new")
		other, err := s.RecordDelivery("gitlab", "delivery-1")
		Ok(t, err)
		Assert(t, other, "delivery IDs from different providers collided")

		// A delivery that was not handled can be retried.
		Ok(t, s.ForgetDelivery("github", "delivery-1"))
		retried, err := s.RecordDelivery("github", "delivery-1")
		Ok(t, err)
		Assert(t, retried, "forgotten delivery reported as duplicate")

		_, err = s.RecordDelivery("github", "")
		ErrContains(t, "invalid webhook delivery ID", err)
		long := strings.Repeat("a", maxDeliveryID+1)
		first, err = s.RecordDelivery("github", long)
		Ok(t, err)
		Assert(t, first, "new long delivery reported as duplicate")
		again, err = s.RecordDelivery("github", long)
		Ok(t, err)
		Assert(t, !again, "duplicate long delivery reported as new")
	})
}

func TestWebhookDeliveryRetention(t *testing.T) {
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) {
		s := d.Store(t)
		_, err := s.RecordDelivery("github", "old")
		Ok(t, err)
		Ok(t, s.db.Model(&webhookDelivery{}).Where("delivery_id = ?", "old").Update("received_at", databaseTime(time.Now().Add(-DeliveryRetention-time.Hour))).Error)
		_, err = s.RecordDelivery("github", "recent")
		Ok(t, err)
		now, err := s.Now()
		Ok(t, err)
		Ok(t, s.CollectDeliveries(now, DeliveryRetention))
		var kept []string
		Ok(t, s.db.Model(&webhookDelivery{}).Order("delivery_id").Pluck("delivery_id", &kept).Error)
		Equals(t, []string{"recent"}, kept)
	})
}

// Servers sharing a database that receive the same delivery at once handle it
// on exactly one of them.
func TestConcurrentDuplicateDeliveries(t *testing.T) {
	d := PostgresDatabase(t)
	stores := []*Store{d.Store(t), d.OtherServer(t).Store(t)}
	for round := range 20 {
		id := "delivery-" + string(rune('a'+round))
		var accepted atomic.Int32
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				first, err := stores[i%2].RecordDelivery("github", id)
				if err != nil {
					t.Error(err)
				}
				if first {
					accepted.Add(1)
				}
			}()
		}
		wg.Wait()
		Equals(t, int32(1), accepted.Load())
	}
}
