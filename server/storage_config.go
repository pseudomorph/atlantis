// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"fmt"
)

// StorageConfig is the structured storage section of the server config file.
type StorageConfig struct {
	Database StorageDatabaseConfig `mapstructure:"database"`
}

// StorageDatabaseConfig selects the database that owns application records.
type StorageDatabaseConfig struct {
	Type string `mapstructure:"type"`
}

// Validate rejects unsupported or conflicting storage settings.
func (c StorageConfig) Validate() error {
	if c.Database.Type != "sqlite" {
		return fmt.Errorf("storage.database.type must be sqlite")
	}
	return nil
}
