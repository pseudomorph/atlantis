// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/runatlantis/atlantis/server/core/artifacts"
)

type StorageConfig struct {
	WorkflowFiles []string               `mapstructure:"workflow_files"`
	Database      StorageDatabaseConfig  `mapstructure:"database"`
	Artifacts     StorageArtifactsConfig `mapstructure:"artifacts"`
	Retention     StorageRetentionConfig `mapstructure:"retention"`
}
type StorageDatabaseConfig struct {
	Type    string `mapstructure:"type"`
	DSN     string `mapstructure:"dsn"`
	DSNEnv  string `mapstructure:"dsn_env"`
	DSNFile string `mapstructure:"dsn_file"`
}
type StorageArtifactsConfig struct {
	Type      string          `mapstructure:"type"`
	Directory string          `mapstructure:"directory"`
	S3        StorageS3Config `mapstructure:"s3"`
}
type StorageS3Config struct {
	Bucket         string `mapstructure:"bucket"`
	Region         string `mapstructure:"region"`
	Prefix         string `mapstructure:"prefix"`
	Endpoint       string `mapstructure:"endpoint"`
	ForcePathStyle bool   `mapstructure:"force_path_style"`
	Profile        string `mapstructure:"profile"`
}
type StorageRetentionConfig struct {
	// Logs is measured from PR closure, or completion for jobs without a PR.
	Logs string `mapstructure:"logs"`
	// PlanArtifacts covers plans, showfiles, and workflow files after apply or invalidation.
	PlanArtifacts string `mapstructure:"plan_artifacts"`
}

func (c StorageConfig) Validate() error {
	for _, file := range c.WorkflowFiles {
		if !filepath.IsLocal(file) || file == "." {
			return fmt.Errorf("storage.workflow_files must contain relative file paths")
		}
	}
	switch c.Database.Type {
	case "sqlite":
		if c.Database.DSN != "" || c.Database.DSNEnv != "" || c.Database.DSNFile != "" {
			return fmt.Errorf("storage.database: sqlite uses data-dir and does not accept a DSN")
		}
	case "postgres":
		sources := 0
		for _, value := range []string{c.Database.DSN, c.Database.DSNEnv, c.Database.DSNFile} {
			if value != "" {
				sources++
			}
		}
		if sources != 1 {
			return fmt.Errorf("storage.database: postgres requires exactly one of dsn, dsn_env, or dsn_file")
		}
	default:
		return fmt.Errorf("storage.database.type must be sqlite or postgres")
	}
	switch c.Artifacts.Type {
	case "", "database":
		if c.Artifacts.Directory != "" || c.Artifacts.S3 != (StorageS3Config{}) {
			return fmt.Errorf("storage.artifacts: database does not accept filesystem or S3 settings")
		}
	case "filesystem":
		if c.Artifacts.S3 != (StorageS3Config{}) {
			return fmt.Errorf("storage.artifacts: filesystem does not accept S3 settings")
		}
	case "s3":
		if prefix := strings.TrimSuffix(c.Artifacts.S3.Prefix, "/"); prefix != "" && !artifacts.ValidKey(prefix) {
			return fmt.Errorf("storage.artifacts.s3.prefix must be a relative object prefix")
		}
		if c.Artifacts.S3.Bucket == "" || c.Artifacts.Directory != "" {
			return fmt.Errorf("storage.artifacts: s3 requires a bucket and does not accept directory")
		}
	default:
		return fmt.Errorf("storage.artifacts.type must be database, filesystem, or s3")
	}
	_, _, err := c.Durations()
	return err
}
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
func (c StorageConfig) ArtifactDirectory(dataDir string) string {
	if c.Artifacts.Directory != "" {
		return c.Artifacts.Directory
	}
	return filepath.Join(dataDir, "artifacts")
}
func (c StorageConfig) ConnectionString() (string, error) {
	if c.Database.DSNFile != "" {
		value, err := os.ReadFile(c.Database.DSNFile)
		if err != nil {
			return "", fmt.Errorf("reading storage.database.dsn_file")
		}
		dsn := strings.TrimSpace(string(value))
		if dsn == "" {
			return "", fmt.Errorf("storage.database.dsn_file is empty")
		}
		return dsn, nil
	}

	if c.Database.DSNEnv != "" {
		dsn := os.Getenv(c.Database.DSNEnv)
		if dsn == "" {
			return "", fmt.Errorf("storage.database.dsn_env names an empty or missing environment variable")
		}
		return dsn, nil
	}
	return c.Database.DSN, nil
}
