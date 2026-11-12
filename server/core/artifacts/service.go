// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
)

// Service captures completed plan workflows and restores their immutable inputs.
type Service struct {
	WorkflowFiles []string
	Records       db.SnapshotStore
	Blobs         SnapshotBackend
}

const DependencyLockFile = ".terraform.lock.hcl"

type snapshotSource struct {
	kind, path string
	data       []byte
	generated  bool
}

// SnapshotPlan captures the final workflow output before the job becomes pending.
func (s *Service) SnapshotPlan(ctx command.ProjectContext, directory, planPath string) error {
	return s.snapshotPlan(ctx, directory, planPath, "", nil)
}
func (s *Service) snapshotPlan(ctx command.ProjectContext, directory, planPath, dataDirectory string, extra []snapshotSource) error {
	if ctx.ArtifactRoot == "" {
		return fmt.Errorf("snapshot source scope is missing")
	}
	root, err := os.OpenRoot(ctx.ArtifactRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	var sources []snapshotSource
	captured := map[string]bool{}
	add := func(kind, name string, optional bool) error {
		relative, e := filepath.Rel(ctx.ArtifactRoot, name)
		if e != nil || !filepath.IsLocal(relative) || relative == "." {
			return fmt.Errorf("snapshot source is outside assigned checkout")
		}
		relative = filepath.ToSlash(relative)
		if kind == "workflow" && captured[relative] {
			return nil
		}
		if kind == "workflow" {
			if dataDirectory != "" {
				dataPath, err := filepath.Rel(ctx.ArtifactRoot, filepath.Join(directory, dataDirectory))
				if err != nil {
					return err
				}
				dataPath = filepath.ToSlash(dataPath)
				if relative == dataPath || strings.HasPrefix(relative, dataPath+"/") {
					return fmt.Errorf("workflow snapshot includes Terraform data directory")
				}
			}
			for _, part := range strings.Split(relative, "/") {
				if part == ".git" || part == ".terraform" || strings.HasSuffix(part, ".tfstate") || strings.HasSuffix(part, ".tfstate.backup") {
					return fmt.Errorf("workflow snapshot path %q contains excluded state or cache data", relative)
				}
			}
		}
		info, e := root.Lstat(relative)
		if optional && os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("snapshot symlink %q is unsupported", relative)
		}
		captured[relative] = true
		sources = append(sources, snapshotSource{kind: kind, path: relative})
		return nil
	}
	for _, f := range []struct{ kind, name string }{{"plan", planPath}, {"show", filepath.Join(directory, ctx.GetShowResultFileName())}, {"lockfile", filepath.Join(directory, DependencyLockFile)}} {
		if err := add(f.kind, f.name, true); err != nil {
			return err
		}
	}
	var walk func(string) error
	walk = func(name string) error {
		rel, e := filepath.Rel(ctx.ArtifactRoot, name)
		if e != nil || !filepath.IsLocal(rel) {
			return fmt.Errorf("invalid workflow artifact path")
		}
		info, e := root.Lstat(rel)
		if e != nil {
			return e
		}
		if e = add("workflow", name, false); e != nil {
			return e
		}
		if info.IsDir() {
			dir, e := root.Open(rel)
			if e != nil {
				return e
			}
			entries, e := dir.ReadDir(-1)
			closeErr := dir.Close()
			if e != nil {
				return e
			}
			if closeErr != nil {
				return closeErr
			}
			for _, entry := range entries {
				if e := walk(filepath.Join(name, entry.Name())); e != nil {
					return e
				}
			}
		}
		return nil
	}
	for _, file := range s.WorkflowFiles {
		if !filepath.IsLocal(file) || file == "." {
			return fmt.Errorf("invalid workflow artifact path")
		}
		name := filepath.Join(directory, file)
		rel, e := filepath.Rel(ctx.ArtifactRoot, name)
		if e != nil {
			return e
		}
		if _, e = root.Lstat(rel); os.IsNotExist(e) {
			continue
		} else if e != nil {
			return e
		}
		if e = walk(name); e != nil {
			return e
		}
	}
	if ctx.RequiresAtlantisManagedPlanFile {
		havePlan := false
		for _, source := range sources {
			if source.kind == "plan" {
				havePlan = true
			}
		}
		if !havePlan {
			return fmt.Errorf("managed plan file is missing")
		}
	}
	sources = append(sources, extra...)
	haveOutput := false
	for _, source := range sources {
		if source.kind == "plan" || source.kind == "workflow" {
			haveOutput = true
		}
	}
	if !haveOutput {
		return fmt.Errorf("plan workflow produced no recoverable output; declare custom files or directories in storage.workflow_files")
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].path < sources[j].path })
	return s.publishSnapshot(ctx.JobID, root, dataDirectory, sources)
}

// InputSnapshot returns the manifest bound to a consuming job.
func (s *Service) InputSnapshot(jobID string) (*models.PlanSnapshot, error) {
	return s.Records.InputSnapshot(jobID)
}
