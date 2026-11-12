// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/go-version"

	"github.com/runatlantis/atlantis/server/events/command"
)

// PlanRuntime binds initialization metadata to the completed plan's artifacts.
type PlanRuntime struct {
	DataDirectory string `json:"data_directory"`
}

// SnapshotPlanWithEnvironment saves provider selections and backend initialization
// metadata, never Terraform's actual state or installed provider directories.
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
	if _, err := os.Stat(filepath.Join(directory, DependencyLockFile)); os.IsNotExist(err) {
		return s.SnapshotPlan(ctx, directory, planPath)
	} else if err != nil {
		return err
	}
	workspace := ctx.Workspace
	if workspace == "" {
		workspace = "default"
	}
	selectedWorkspace, set := envs["TF_WORKSPACE"]
	if !set {
		selectedWorkspace = os.Getenv("TF_WORKSPACE")
	}
	if selectedWorkspace != "" && selectedWorkspace != workspace {
		return fmt.Errorf("TF_WORKSPACE differs from the planned project workspace")
	}
	dataDir, err := command.TerraformDataDirectory(directory, envs)
	if err != nil {
		return err
	}
	if err := s.SnapshotPlan(ctx, directory, planPath); err != nil {
		return err
	}
	job, err := s.Records.Job(ctx.JobID)
	if err != nil {
		return err
	}
	if job == nil {
		return fmt.Errorf("plan job is missing")
	}
	backend := filepath.Join(dataDir, "terraform.tfstate")
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	data, err := root.ReadFile(backend)
	if err == nil {
		var metadata struct {
			Backend json.RawMessage `json:"backend"`
		}
		if json.Unmarshal(data, &metadata) != nil || len(metadata.Backend) == 0 || string(metadata.Backend) == "null" {
			return fmt.Errorf("TF_DATA_DIR contains unsupported backend metadata; refusing to capture Terraform state")
		}
		relative, err := filepath.Rel(ctx.ArtifactRoot, filepath.Join(directory, backend))
		if err != nil || !filepath.IsLocal(relative) {
			return fmt.Errorf("backend metadata is outside its assigned checkout")
		}
		if err := s.Publish(*job, "backend", relative, bytes.NewReader(data)); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	workspacePath, err := filepath.Rel(ctx.ArtifactRoot, filepath.Join(directory, dataDir, "environment"))
	if err != nil || !filepath.IsLocal(workspacePath) {
		return fmt.Errorf("workspace metadata is outside its assigned checkout")
	}
	if err := s.Publish(*job, "workspace", workspacePath, strings.NewReader(workspace)); err != nil {
		return err
	}
	manifest, err := json.Marshal(PlanRuntime{DataDirectory: dataDir})
	if err != nil {
		return err
	}
	return s.Publish(*job, "runtime", "", bytes.NewReader(manifest))
}

// InputRuntime verifies the immutable initialization manifest selected for a consumer.
func (s *Service) InputRuntime(jobID string) (*PlanRuntime, error) {
	inputs, err := s.Records.InputArtifacts(jobID)
	if err != nil {
		return nil, err
	}
	for _, a := range inputs {
		if a.Kind != "runtime" {
			continue
		}
		if a.Size > 64*1024 {
			return nil, fmt.Errorf("plan runtime manifest is too large")
		}
		r, err := s.openArtifact(a)
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(r, 64*1024+1))
		closeErr := r.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if int64(len(data)) != a.Size || fmt.Sprintf("%x", sha256.Sum256(data)) != a.SHA256 {
			return nil, fmt.Errorf("plan runtime manifest checksum mismatch")
		}
		var runtime PlanRuntime
		if err := json.Unmarshal(data, &runtime); err != nil {
			return nil, err
		}
		if !filepath.IsLocal(runtime.DataDirectory) || runtime.DataDirectory == "." {
			return nil, fmt.Errorf("invalid restored TF_DATA_DIR")
		}
		return &runtime, nil
	}
	return nil, fmt.Errorf("pending plan has no initialization manifest; run plan again")
}
