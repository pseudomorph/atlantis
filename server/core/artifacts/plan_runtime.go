// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/go-version"
	"github.com/runatlantis/atlantis/server/events/command"
)

// PlanRuntime binds the initialization directory to the completed plan.
type PlanRuntime struct {
	DataDirectory string `json:"data_directory"`
}

// SnapshotPlanWithEnvironment saves initialization metadata, never provider caches
// or actual Terraform state, in the same publication as the plan.
func (s *Service) SnapshotPlanWithEnvironment(ctx command.ProjectContext, directory, planPath string, envs map[string]string) error {
	if !ctx.RequiresAtlantisManagedPlanFile {
		return s.SnapshotPlan(ctx, directory, planPath)
	}
	if _, err := os.Stat(planPath); os.IsNotExist(err) {
		return s.SnapshotPlan(ctx, directory, planPath)
	} else if err != nil {
		return err
	}
	if ctx.TerraformVersion == nil || (ctx.TerraformDistribution != nil && *ctx.TerraformDistribution == "terraform" && ctx.TerraformVersion.Core().LessThan(version.Must(version.NewVersion("0.14.0")))) {
		return s.SnapshotPlan(ctx, directory, planPath)
	}
	workspace := ctx.Workspace
	if workspace == "" {
		workspace = "default"
	}
	selected, set := envs["TF_WORKSPACE"]
	if !set {
		selected = os.Getenv("TF_WORKSPACE")
	}
	if selected != "" && selected != workspace {
		return fmt.Errorf("TF_WORKSPACE differs from the planned project workspace")
	}
	dataDir, err := command.TerraformDataDirectory(directory, envs)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(ctx.ArtifactRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	relative := func(name string) (string, error) {
		p, e := filepath.Rel(ctx.ArtifactRoot, filepath.Join(directory, name))
		if e != nil || !filepath.IsLocal(p) {
			return "", fmt.Errorf("initialization metadata is outside assigned checkout")
		}
		return filepath.ToSlash(p), nil
	}
	backend, err := relative(filepath.Join(dataDir, "terraform.tfstate"))
	if err != nil {
		return err
	}
	var extra []snapshotSource
	data, err := readSnapshotSource(root, backend)
	if err == nil {
		var metadata map[string]json.RawMessage
		if json.Unmarshal(data, &metadata) != nil || len(metadata["backend"]) == 0 || string(metadata["backend"]) == "null" {
			return fmt.Errorf("TF_DATA_DIR contains unsupported backend metadata; refusing to capture Terraform state")
		}
		for _, field := range []string{"resources", "outputs", "check_results"} {
			if _, exists := metadata[field]; exists {
				return fmt.Errorf("TF_DATA_DIR contains Terraform state field %q; refusing to capture Terraform state", field)
			}
		}
		extra = append(extra, snapshotSource{kind: "backend", path: backend, data: data, generated: true})
	} else if !os.IsNotExist(err) {
		return err
	}
	selection, err := relative(filepath.Join(dataDir, "environment"))
	if err != nil {
		return err
	}
	extra = append(extra, snapshotSource{kind: "workspace", path: selection, data: []byte(workspace), generated: true})
	return s.snapshotPlan(ctx, directory, planPath, filepath.ToSlash(dataDir), extra)
}

func (s *Service) InputRuntime(jobID string) (*PlanRuntime, error) {
	snapshot, err := s.Records.InputSnapshot(jobID)
	if err != nil {
		return nil, err
	}
	if snapshot == nil || snapshot.Manifest.DataDirectory == "" {
		return nil, fmt.Errorf("pending plan has no initialization manifest; run plan again")
	}
	if err := snapshot.Manifest.Validate(); err != nil {
		return nil, err
	}
	return &PlanRuntime{DataDirectory: snapshot.Manifest.DataDirectory}, nil
}
