// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package command

import (
	"fmt"
	"os"
	"path/filepath"
)

// TerraformDataDirectory returns a project-local data directory for portable restoration.
func TerraformDataDirectory(project string, envs map[string]string) (string, error) {
	value, set := envs["TF_DATA_DIR"]
	if !set {
		value = os.Getenv("TF_DATA_DIR")
	}
	if value == "" {
		value = ".terraform"
	}
	if filepath.IsAbs(value) {
		var err error
		value, err = filepath.Rel(project, value)
		if err != nil {
			return "", fmt.Errorf("TF_DATA_DIR cannot be restored within the project")
		}
	}
	value = filepath.Clean(value)
	if !filepath.IsLocal(value) || value == "." {
		return "", fmt.Errorf("durable plan restoration requires TF_DATA_DIR inside the project directory")
	}
	return value, nil
}
