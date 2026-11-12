// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package runtime_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hashicorp/go-version"
	"github.com/runatlantis/atlantis/server/core/runtime"
	"github.com/runatlantis/atlantis/server/core/terraform"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

type restoredInitExecutor struct {
	calls int
	args  []string
}

func (e *restoredInitExecutor) EnsureVersion(logging.SimpleLogging, terraform.Distribution, *version.Version) error {
	return nil
}
func (e *restoredInitExecutor) RunCommandWithVersion(_ command.ProjectContext, _ string, args []string, _ map[string]string, _ terraform.Distribution, _ *version.Version, _ string) (string, error) {
	e.calls++
	e.args = append([]string(nil), args...)
	return "", nil
}

func TestRestoredInitPreservesProviderSelections(t *testing.T) {
	for _, tc := range []struct {
		tool, version string
		readonly      bool
	}{
		{"terraform", "0.14.11", false}, {"terraform", "0.15.0", true}, {"terraform", "1.1.0", true}, {"terraform", "1.15.0-rc1", true}, {"opentofu", "1.6.0", true},
	} {
		t.Run(tc.tool+tc.version, func(t *testing.T) {
			path := t.TempDir()
			output, err := exec.Command("git", "init", path).CombinedOutput()
			if err != nil {
				t.Fatalf("initializing Git fixture: %v: %s", err, output)
			}
			lock := filepath.Join(path, ".terraform.lock.hcl")
			Ok(t, os.WriteFile(lock, []byte("recorded provider selections"), 0600))
			executor := &restoredInitExecutor{}
			runner := &runtime.InitStepRunner{TerraformExecutor: executor, DefaultTFDistribution: terraform.NewDistribution(tc.tool), DefaultTFVersion: version.Must(version.NewVersion(tc.version))}
			ctx := command.ProjectContext{Log: logging.NewNoopLogger(t), RestoredDataDirectory: ".terraform", RestoredLockHash: fmt.Sprintf("%x", sha256.Sum256([]byte("recorded provider selections"))), Workspace: "default"}
			_, err = runner.Run(ctx, nil, path, nil)
			Ok(t, err)
			want := []string{"init", "-input=false"}
			if tc.readonly {
				want = append(want, "-lockfile=readonly")
			}
			Assert(t, reflect.DeepEqual(want, executor.args), "init arguments = %v, want %v", executor.args, want)
			data, err := os.ReadFile(lock)
			Ok(t, err)
			Equals(t, "recorded provider selections", string(data))
		})
	}
}

func TestRestoredInitRejectsUnsafeSetup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		envs    map[string]string
		missing bool
	}{
		{name: "upgrade", args: []string{"-upgrade"}},
		{name: "expanded upgrade", args: []string{"-${INIT_MODE}"}, envs: map[string]string{"INIT_MODE": "upgrade"}},
		{name: "rewrite lock", args: []string{"-lockfile=update"}},
		{name: "migrate backend", args: []string{"-migrate-state"}},
		{name: "changed data directory", envs: map[string]string{"TF_DATA_DIR": "other"}},
		{name: "external data directory", envs: map[string]string{"TF_DATA_DIR": "../outside"}},
		{name: "environment upgrade", envs: map[string]string{"TF_CLI_ARGS_init": "-upgrade"}},
		{name: "missing lock", missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir()
			if !tc.missing {
				Ok(t, os.WriteFile(filepath.Join(path, ".terraform.lock.hcl"), []byte("locked"), 0600))
			}
			executor := &restoredInitExecutor{}
			runner := &runtime.InitStepRunner{TerraformExecutor: executor, DefaultTFDistribution: terraform.NewDistributionTerraform(), DefaultTFVersion: version.Must(version.NewVersion("1.1.0"))}
			_, err := runner.Run(command.ProjectContext{Log: logging.NewNoopLogger(t), RestoredDataDirectory: ".terraform"}, tc.args, path, tc.envs)
			Assert(t, err != nil, "unsafe restored initialization was accepted")
			Equals(t, 0, executor.calls)
		})
	}
}

func TestRestoredInitAllowsBackendValuesContainingFlagNames(t *testing.T) {
	path := t.TempDir()
	Ok(t, os.WriteFile(filepath.Join(path, ".terraform.lock.hcl"), []byte("locked"), 0600))
	executor := &restoredInitExecutor{}
	runner := &runtime.InitStepRunner{TerraformExecutor: executor, DefaultTFDistribution: terraform.NewDistributionTerraform(), DefaultTFVersion: version.Must(version.NewVersion("1.1.0"))}
	_, err := runner.Run(command.ProjectContext{Log: logging.NewNoopLogger(t), RestoredDataDirectory: ".terraform"}, []string{"-backend-config=key=pre-upgrade.tfstate"}, path, nil)
	Ok(t, err)
	Equals(t, 1, executor.calls)
}

func TestRestoredInitRejectsChangedLockAndOldPrerelease(t *testing.T) {
	for _, versionString := range []string{"1.1.0", "0.13.0-rc1"} {
		t.Run(versionString, func(t *testing.T) {
			path := t.TempDir()
			Ok(t, os.WriteFile(filepath.Join(path, ".terraform.lock.hcl"), []byte("modified"), 0600))
			executor := &restoredInitExecutor{}
			runner := &runtime.InitStepRunner{TerraformExecutor: executor, DefaultTFDistribution: terraform.NewDistributionTerraform(), DefaultTFVersion: version.Must(version.NewVersion(versionString))}
			ctx := command.ProjectContext{Log: logging.NewNoopLogger(t), RestoredDataDirectory: ".terraform"}
			if versionString == "1.1.0" {
				ctx.RestoredLockHash = fmt.Sprintf("%x", sha256.Sum256([]byte("original")))
			}
			_, err := runner.Run(ctx, nil, path, nil)
			Assert(t, err != nil, "unsafe initialization accepted")
			Equals(t, 0, executor.calls)
		})
	}
}
