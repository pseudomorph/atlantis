// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"crypto/sha256"
	"fmt"
	"time"

	"gorm.io/gorm/clause"
)

// DeliveryRetention is how long a webhook delivery ID is remembered. It covers
// provider retries and manual redelivery of recent deliveries.
const DeliveryRetention = 7 * 24 * time.Hour

// maxDeliveryID bounds the delivery IDs stored as sent. Providers send UUIDs;
// a longer ID is stored as its SHA-256 digest.
const maxDeliveryID = 128

func deliveryKey(id string) string {
	if len(id) <= maxDeliveryID {
		return id
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(id)))
}

type webhookDelivery struct {
	Provider   string `gorm:"primaryKey"`
	DeliveryID string `gorm:"primaryKey"`
	ReceivedAt time.Time
}

func (webhookDelivery) TableName() string { return "webhook_deliveries" }

// RecordDelivery records a webhook delivery and reports whether it is new. One
// insert decides between servers that receive the same delivery at once.
func (s *Store) RecordDelivery(provider, id string) (bool, error) {
	if provider == "" || id == "" {
		return false, fmt.Errorf("invalid webhook delivery ID")
	}
	now, err := databaseNow(s.db)
	if err != nil {
		return false, err
	}
	result := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&webhookDelivery{Provider: provider, DeliveryID: deliveryKey(id), ReceivedAt: now})
	return result.RowsAffected == 1, result.Error
}

// ForgetDelivery removes a delivery that was not handled, so the provider's
// retry of it is processed.
func (s *Store) ForgetDelivery(provider, id string) error {
	return s.db.Where("provider = ? AND delivery_id = ?", provider, deliveryKey(id)).Delete(&webhookDelivery{}).Error
}

// CollectDeliveries deletes deliveries received more than retention before now.
func (s *Store) CollectDeliveries(now time.Time, retention time.Duration) error {
	return s.db.Where("received_at < ?", databaseTime(now.Add(-retention))).Delete(&webhookDelivery{}).Error
}
