// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// databaseNow reads wall-clock time on the current connection, including after a lock wait.
// Call explicitly; a query from GORM's NowFunc would re-enter SQLite's single connection.
func databaseNow(tx *gorm.DB) (time.Time, error) {
	var query string
	switch tx.Dialector.Name() {
	case "sqlite":
		query = "SELECT strftime('%Y-%m-%d %H:%M:%f', 'now')"
	default:
		return time.Time{}, fmt.Errorf("unsupported database clock %q", tx.Dialector.Name())
	}
	var value any
	if err := tx.Raw(query).Row().Scan(&value); err != nil {
		return time.Time{}, fmt.Errorf("reading database clock: %w", err)
	}
	var text string
	switch value := value.(type) {
	case time.Time:
		return databaseTime(value), nil
	case string:
		text = value
	case []byte:
		text = string(value)
	default:
		return time.Time{}, fmt.Errorf("unexpected database clock type %T", value)
	}
	instant, err := time.ParseInLocation("2006-01-02 15:04:05.999999999", text, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("decoding database clock: %w", err)
	}
	return databaseTime(instant), nil
}
