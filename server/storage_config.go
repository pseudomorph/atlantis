// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"fmt"
	"path/filepath"
	"time"
)

// StorageConfig is the structured storage section of the server config file.
type StorageConfig struct {
	WorkflowFiles []string               `mapstructure:"workflow_files"`
	Database      StorageDatabaseConfig  `mapstructure:"database"`
	Artifacts     StorageArtifactsConfig `mapstructure:"artifacts"`
	Retention     StorageRetentionConfig `mapstructure:"retention"`
}

// StorageDatabaseConfig selects the database that owns application records.
type StorageDatabaseConfig struct {
	Type string `mapstructure:"type"`
}

// StorageArtifactsConfig selects where artifact bytes are stored.
type StorageArtifactsConfig struct {
	Type      string `mapstructure:"type"`
	Directory string `mapstructure:"directory"`
}

// StorageRetentionConfig sets how long finished artifacts are kept.
type StorageRetentionConfig struct {
	// PlanArtifacts covers plans, showfiles, and workflow files after apply or invalidation.
	PlanArtifacts string `mapstructure:"plan_artifacts"`
}

// Validate rejects unsupported or conflicting storage settings.
func (c StorageConfig) Validate() error {
	for _, file := range c.WorkflowFiles {
		if !filepath.IsLocal(file) || file == "." {
			return fmt.Errorf("storage.workflow_files must contain relative file paths")
		}
	}
	if c.Database.Type != "sqlite" {
		return fmt.Errorf("storage.database.type must be sqlite")
	}
	switch c.Artifacts.Type {
	case "", "database":
		if c.Artifacts.Directory != "" {
			return fmt.Errorf("storage.artifacts: database does not accept a directory")
		}
	case "filesystem":
	default:
		return fmt.Errorf("storage.artifacts.type must be database or filesystem")
	}
	_, err := c.PlanArtifactRetention()
	return err
}

// PlanArtifactRetention returns how long plan artifacts outlive their pending plan.
func (c StorageConfig) PlanArtifactRetention() (time.Duration, error) {
	if c.Retention.PlanArtifacts == "" {
		return 0, nil
	}
	retention, err := time.ParseDuration(c.Retention.PlanArtifacts)
	if err != nil || retention < 0 {
		return 0, fmt.Errorf("storage.retention.plan_artifacts must be a nonnegative duration")
	}
	return retention, nil
}

// ArtifactDirectory returns the filesystem backend directory, defaulting under dataDir.
func (c StorageConfig) ArtifactDirectory(dataDir string) string {
	if c.Artifacts.Directory != "" {
		return c.Artifacts.Directory
	}
	return filepath.Join(dataDir, "artifacts")
}
