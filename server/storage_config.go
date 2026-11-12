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
	// Logs is measured from PR closure, or completion for jobs without a PR.
	Logs string `mapstructure:"logs"`
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
	_, _, err := c.Durations()
	return err
}

// Durations returns the log and plan-artifact retention periods.
func (c StorageConfig) Durations() (time.Duration, time.Duration, error) {
	logs, planArtifacts := 7*24*time.Hour, time.Duration(0)
	var err error
	if c.Retention.Logs != "" {
		logs, err = time.ParseDuration(c.Retention.Logs)
		if err != nil || logs <= 0 {
			return 0, 0, fmt.Errorf("storage.retention.logs must be a positive duration")
		}
	}
	if c.Retention.PlanArtifacts != "" {
		planArtifacts, err = time.ParseDuration(c.Retention.PlanArtifacts)
		if err != nil || planArtifacts < 0 {
			return 0, 0, fmt.Errorf("storage.retention.plan_artifacts must be a nonnegative duration")
		}
	}
	return logs, planArtifacts, nil
}

// ArtifactDirectory returns the filesystem backend directory, defaulting under dataDir.
func (c StorageConfig) ArtifactDirectory(dataDir string) string {
	if c.Artifacts.Directory != "" {
		return c.Artifacts.Directory
	}
	return filepath.Join(dataDir, "artifacts")
}
