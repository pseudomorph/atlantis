// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package runtime_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	version "github.com/hashicorp/go-version"
	. "github.com/petergtz/pegomock/v4"
	"github.com/runatlantis/atlantis/server/core/runtime"
	tf "github.com/runatlantis/atlantis/server/core/terraform"
	"github.com/runatlantis/atlantis/server/core/terraform/mocks"
	tfclientmocks "github.com/runatlantis/atlantis/server/core/terraform/tfclient/mocks"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

func TestRun_UsesGetOrInitForRightVersion(t *testing.T) {
	RegisterMockTestingT(t)
	mockDownloader := mocks.NewMockDownloader()
	tfDistribution := tf.NewDistributionTerraformWithDownloader(mockDownloader)
	cases := []struct {
		version string
		expCmd  string
	}{
		{
			"0.8.9",
			"get",
		},
		{
			"0.9.0",
			"init",
		},
		{
			"0.9.1",
			"init",
		},
		{
			"0.10.0",
			"init",
		},
	}

	for _, c := range cases {
		t.Run(c.version, func(t *testing.T) {
			terraform := tfclientmocks.NewMockClient()

			logger := logging.NewNoopLogger(t)
			ctx := command.ProjectContext{
				Workspace:  "workspace",
				RepoRelDir: ".",
				Log:        logger,
			}
			// extra_args is marked expandable on the context the client receives.
			ctx.ExpandableArgs = []string{"extra", "args"}

			tfVersion, _ := version.NewVersion(c.version)
			iso := runtime.InitStepRunner{
				TerraformExecutor:     terraform,
				DefaultTFDistribution: tfDistribution,
				DefaultTFVersion:      tfVersion,
			}
			When(terraform.RunCommandWithVersion(Any[command.ProjectContext](), Any[string](), Any[[]string](), Any[map[string]string](), Any[tf.Distribution](), Any[*version.Version](), Any[string]())).
				ThenReturn("output", nil)

			output, err := iso.Run(ctx, []string{"extra", "args"}, "/path", map[string]string(nil))
			Ok(t, err)
			// When there is no error, should not return init output to PR.
			Equals(t, "", output)

			// If using init then we specify -input=false but not for get.
			expArgs := []string{c.expCmd, "-input=false", "-upgrade", "extra", "args"}
			if c.expCmd == "get" {
				expArgs = []string{c.expCmd, "-upgrade", "extra", "args"}
			}
			terraform.VerifyWasCalledOnce().RunCommandWithVersion(ctx, "/path", expArgs, map[string]string(nil), tfDistribution, tfVersion, "workspace")
		})
	}
}

func TestInitStepRunner_IgnoresCommentArgsForExpansionPolicy(t *testing.T) {
	RegisterMockTestingT(t)
	terraform := tfclientmocks.NewMockClient()
	logger := logging.NewNoopLogger(t)
	tfVersion := version.Must(version.NewVersion("1.14.0"))
	mockDownloader := mocks.NewMockDownloader()
	tfDistribution := tf.NewDistributionTerraformWithDownloader(mockDownloader)

	const backendConfigArg = "-backend-config=$ATLANTIS_BACKEND_CONFIG"
	ctx := command.ProjectContext{
		Workspace:   "default",
		RepoRelDir:  ".",
		Log:         logger,
		CommentArgs: []string{backendConfigArg},
	}
	execCtx := ctx
	execCtx.ExpandableArgs = []string{backendConfigArg}
	execCtx.CommentArgs = nil

	runner := runtime.InitStepRunner{
		TerraformExecutor:     terraform,
		DefaultTFDistribution: tfDistribution,
		DefaultTFVersion:      tfVersion,
	}
	When(terraform.RunCommandWithVersion(Any[command.ProjectContext](), Any[string](), Any[[]string](), Any[map[string]string](), Any[tf.Distribution](), Any[*version.Version](), Any[string]())).
		ThenReturn("", nil)

	path := t.TempDir()
	output, err := runner.Run(ctx, []string{backendConfigArg}, path, nil)
	Ok(t, err)
	Equals(t, "", output)

	terraform.VerifyWasCalledOnce().RunCommandWithVersion(
		execCtx,
		path,
		[]string{"init", "-input=false", "-upgrade", backendConfigArg},
		map[string]string(nil),
		tfDistribution,
		tfVersion,
		"default",
	)
}

func TestInitStepRunner_TestRun_UsesConfiguredDistribution(t *testing.T) {
	RegisterMockTestingT(t)
	mockDownloader := mocks.NewMockDownloader()
	tfDistribution := tf.NewDistributionTerraformWithDownloader(mockDownloader)
	cases := []struct {
		version      string
		distribution string
		expCmd       string
	}{
		{
			"0.8.9",
			"opentofu",
			"get",
		},
		{
			"0.8.9",
			"terraform",
			"get",
		},
		{
			"0.9.0",
			"opentofu",
			"init",
		},
		{
			"0.9.1",
			"terraform",
			"init",
		},
	}

	for _, c := range cases {
		t.Run(c.version, func(t *testing.T) {
			terraform := tfclientmocks.NewMockClient()

			logger := logging.NewNoopLogger(t)
			ctx := command.ProjectContext{
				Workspace:             "workspace",
				RepoRelDir:            ".",
				Log:                   logger,
				TerraformDistribution: &c.distribution,
			}
			// extra_args is marked expandable on the context the client receives.
			ctx.ExpandableArgs = []string{"extra", "args"}

			tfVersion, _ := version.NewVersion(c.version)
			iso := runtime.InitStepRunner{
				TerraformExecutor:     terraform,
				DefaultTFDistribution: tfDistribution,
				DefaultTFVersion:      tfVersion,
			}
			When(terraform.RunCommandWithVersion(Any[command.ProjectContext](), Any[string](), Any[[]string](), Any[map[string]string](), Any[tf.Distribution](), Any[*version.Version](), Any[string]())).
				ThenReturn("output", nil)

			output, err := iso.Run(ctx, []string{"extra", "args"}, "/path", map[string]string(nil))
			Ok(t, err)
			// When there is no error, should not return init output to PR.
			Equals(t, "", output)

			// If using init then we specify -input=false but not for get.
			expArgs := []string{c.expCmd, "-input=false", "-upgrade", "extra", "args"}
			if c.expCmd == "get" {
				expArgs = []string{c.expCmd, "-upgrade", "extra", "args"}
			}
			terraform.VerifyWasCalledOnce().RunCommandWithVersion(Eq(ctx), Eq("/path"), Eq(expArgs), Eq(map[string]string(nil)), NotEq(tfDistribution), Eq(tfVersion), Eq("workspace"))
		})
	}
}

func TestRun_ShowInitOutputOnError(t *testing.T) {
	// If there was an error during init then we want the output to be returned.
	RegisterMockTestingT(t)
	tfClient := tfclientmocks.NewMockClient()
	logger := logging.NewNoopLogger(t)
	When(tfClient.RunCommandWithVersion(Any[command.ProjectContext](), Any[string](), Any[[]string](), Any[map[string]string](), Any[tf.Distribution](), Any[*version.Version](), Any[string]())).
		ThenReturn("output", errors.New("error"))
	mockDownloader := mocks.NewMockDownloader()
	tfDistribution := tf.NewDistributionTerraformWithDownloader(mockDownloader)
	tfVersion, _ := version.NewVersion("0.11.0")
	iso := runtime.InitStepRunner{
		TerraformExecutor:     tfClient,
		DefaultTFDistribution: tfDistribution,
		DefaultTFVersion:      tfVersion,
	}

	output, err := iso.Run(command.ProjectContext{
		Workspace:  "workspace",
		RepoRelDir: ".",
		Log:        logger,
	}, nil, "/path", map[string]string(nil))
	ErrEquals(t, "error", err)
	Equals(t, "output", output)
}

func TestRun_InitOmitsUpgradeFlagIfLockFileTracked(t *testing.T) {
	// Initialize the git repo.
	repoDir := initRepo(t)

	lockFilePath := filepath.Join(repoDir, ".terraform.lock.hcl")
	err := os.WriteFile(lockFilePath, nil, 0600)
	Ok(t, err)
	// commit lock file
	runCmd(t, repoDir, "git", "add", ".terraform.lock.hcl")
	runCmd(t, repoDir, "git", "commit", "-m", "add .terraform.lock.hcl")

	logger := logging.NewNoopLogger(t)
	ctx := command.ProjectContext{
		Workspace:  "workspace",
		RepoRelDir: ".",
		Log:        logger,
	}
	// extra_args is marked expandable on the context the client receives.
	ctx.ExpandableArgs = []string{"extra", "args"}

	RegisterMockTestingT(t)
	terraform := tfclientmocks.NewMockClient()
	mockDownloader := mocks.NewMockDownloader()
	tfDistribution := tf.NewDistributionTerraformWithDownloader(mockDownloader)
	tfVersion, _ := version.NewVersion("0.14.0")
	iso := runtime.InitStepRunner{
		TerraformExecutor:     terraform,
		DefaultTFDistribution: tfDistribution,
		DefaultTFVersion:      tfVersion,
	}
	When(terraform.RunCommandWithVersion(Any[command.ProjectContext](), Any[string](), Any[[]string](), Any[map[string]string](), Any[tf.Distribution](), Any[*version.Version](), Any[string]())).
		ThenReturn("output", nil)

	output, err := iso.Run(ctx, []string{"extra", "args"}, repoDir, map[string]string(nil))
	Ok(t, err)
	// When there is no error, should not return init output to PR.
	Equals(t, "", output)

	expectedArgs := []string{"init", "-input=false", "extra", "args"}
	terraform.VerifyWasCalledOnce().RunCommandWithVersion(ctx, repoDir, expectedArgs, map[string]string(nil), tfDistribution, tfVersion, "workspace")
}

func TestRun_InitKeepsUpgradeFlagIfLockFileNotPresent(t *testing.T) {
	tmpDir := t.TempDir()

	RegisterMockTestingT(t)
	terraform := tfclientmocks.NewMockClient()
	logger := logging.NewNoopLogger(t)
	ctx := command.ProjectContext{
		Workspace:  "workspace",
		RepoRelDir: ".",
		Log:        logger,
	}
	// extra_args is marked expandable on the context the client receives.
	ctx.ExpandableArgs = []string{"extra", "args"}
	mockDownloader := mocks.NewMockDownloader()
	tfDistribution := tf.NewDistributionTerraformWithDownloader(mockDownloader)
	tfVersion, _ := version.NewVersion("0.14.0")
	iso := runtime.InitStepRunner{
		TerraformExecutor:     terraform,
		DefaultTFDistribution: tfDistribution,
		DefaultTFVersion:      tfVersion,
	}
	When(terraform.RunCommandWithVersion(Any[command.ProjectContext](), Any[string](), Any[[]string](), Any[map[string]string](), Any[tf.Distribution](), Any[*version.Version](), Any[string]())).
		ThenReturn("output", nil)

	output, err := iso.Run(ctx, []string{"extra", "args"}, tmpDir, map[string]string(nil))
	Ok(t, err)
	// When there is no error, should not return init output to PR.
	Equals(t, "", output)

	expectedArgs := []string{"init", "-input=false", "-upgrade", "extra", "args"}
	terraform.VerifyWasCalledOnce().RunCommandWithVersion(ctx, tmpDir, expectedArgs, map[string]string(nil), tfDistribution, tfVersion, "workspace")
}

func TestRun_InitKeepUpgradeFlagIfLockFilePresentAndTFLessThanPoint14(t *testing.T) {
	tmpDir := t.TempDir()
	lockFilePath := filepath.Join(tmpDir, ".terraform.lock.hcl")
	err := os.WriteFile(lockFilePath, nil, 0600)
	Ok(t, err)

	RegisterMockTestingT(t)
	terraform := tfclientmocks.NewMockClient()

	logger := logging.NewNoopLogger(t)
	ctx := command.ProjectContext{
		Workspace:  "workspace",
		RepoRelDir: ".",
		Log:        logger,
	}
	// extra_args is marked expandable on the context the client receives.
	ctx.ExpandableArgs = []string{"extra", "args"}
	mockDownloader := mocks.NewMockDownloader()
	tfDistribution := tf.NewDistributionTerraformWithDownloader(mockDownloader)
	tfVersion, _ := version.NewVersion("0.13.0")
	iso := runtime.InitStepRunner{
		TerraformExecutor:     terraform,
		DefaultTFDistribution: tfDistribution,
		DefaultTFVersion:      tfVersion,
	}
	When(terraform.RunCommandWithVersion(Any[command.ProjectContext](), Any[string](), Any[[]string](), Any[map[string]string](), Any[tf.Distribution](), Any[*version.Version](), Any[string]())).
		ThenReturn("output", nil)

	output, err := iso.Run(ctx, []string{"extra", "args"}, tmpDir, map[string]string(nil))
	Ok(t, err)
	// When there is no error, should not return init output to PR.
	Equals(t, "", output)

	expectedArgs := []string{"init", "-input=false", "-upgrade", "extra", "args"}
	terraform.VerifyWasCalledOnce().RunCommandWithVersion(ctx, tmpDir, expectedArgs, map[string]string(nil), tfDistribution, tfVersion, "workspace")
}

func TestRun_InitExtraArgsDeDupe(t *testing.T) {
	RegisterMockTestingT(t)
	cases := []struct {
		description  string
		extraArgs    []string
		expectedArgs []string
	}{
		{
			"No extra args",
			[]string{},
			[]string{"init", "-input=false", "-upgrade"},
		},
		{
			"Override -upgrade",
			[]string{"-upgrade=false"},
			[]string{"init", "-input=false", "-upgrade=false"},
		},
		{
			"Override -input",
			[]string{"-input=true"},
			[]string{"init", "-input=true", "-upgrade"},
		},
		{
			"Override -input and -upgrade",
			[]string{"-input=true", "-upgrade=false"},
			[]string{"init", "-input=true", "-upgrade=false"},
		},
		{
			"Non duplicate extra args",
			[]string{"extra", "args"},
			[]string{"init", "-input=false", "-upgrade", "extra", "args"},
		},
		{
			"Override upgrade with extra args",
			[]string{"extra", "args", "-upgrade=false"},
			[]string{"init", "-input=false", "-upgrade=false", "extra", "args"},
		},
	}

	for _, c := range cases {
		t.Run(c.description, func(t *testing.T) {
			terraform := tfclientmocks.NewMockClient()

			logger := logging.NewNoopLogger(t)
			ctx := command.ProjectContext{
				Workspace:  "workspace",
				RepoRelDir: ".",
				Log:        logger,
			}
			// extra_args is marked expandable on the context the client receives.
			if len(c.extraArgs) > 0 {
				ctx.ExpandableArgs = c.extraArgs
			}
			mockDownloader := mocks.NewMockDownloader()
			tfDistribution := tf.NewDistributionTerraformWithDownloader(mockDownloader)
			tfVersion, _ := version.NewVersion("0.10.0")
			iso := runtime.InitStepRunner{
				TerraformExecutor:     terraform,
				DefaultTFDistribution: tfDistribution,
				DefaultTFVersion:      tfVersion,
			}
			When(terraform.RunCommandWithVersion(Any[command.ProjectContext](), Any[string](), Any[[]string](), Any[map[string]string](), Any[tf.Distribution](), Any[*version.Version](), Any[string]())).
				ThenReturn("output", nil)

			output, err := iso.Run(ctx, c.extraArgs, "/path", map[string]string(nil))
			Ok(t, err)
			// When there is no error, should not return init output to PR.
			Equals(t, "", output)

			terraform.VerifyWasCalledOnce().RunCommandWithVersion(ctx, "/path", c.expectedArgs, map[string]string(nil), tfDistribution, tfVersion, "workspace")
		})
	}
}

func TestRun_InitDeletesLockFileIfPresentAndNotTracked(t *testing.T) {
	// Initialize the git repo.
	repoDir := initRepo(t)

	lockFilePath := filepath.Join(repoDir, ".terraform.lock.hcl")
	err := os.WriteFile(lockFilePath, nil, 0600)
	Ok(t, err)

	RegisterMockTestingT(t)
	terraform := tfclientmocks.NewMockClient()

	logger := logging.NewNoopLogger(t)
	mockDownloader := mocks.NewMockDownloader()
	tfDistribution := tf.NewDistributionTerraformWithDownloader(mockDownloader)
	tfVersion, _ := version.NewVersion("0.14.0")

	iso := runtime.InitStepRunner{
		TerraformExecutor:     terraform,
		DefaultTFDistribution: tfDistribution,
		DefaultTFVersion:      tfVersion,
	}
	When(terraform.RunCommandWithVersion(Any[command.ProjectContext](), Any[string](), Any[[]string](), Any[map[string]string](), Any[tf.Distribution](), Any[*version.Version](), Any[string]())).
		ThenReturn("output", nil)

	ctx := command.ProjectContext{
		Workspace:  "workspace",
		RepoRelDir: ".",
		Log:        logger,
	}
	// extra_args is marked expandable on the context the client receives.
	ctx.ExpandableArgs = []string{"extra", "args"}
	output, err := iso.Run(ctx, []string{"extra", "args"}, repoDir, map[string]string(nil))
	Ok(t, err)
	// When there is no error, should not return init output to PR.
	Equals(t, "", output)

	expectedArgs := []string{"init", "-input=false", "-upgrade", "extra", "args"}
	terraform.VerifyWasCalledOnce().RunCommandWithVersion(ctx, repoDir, expectedArgs, map[string]string(nil), tfDistribution, tfVersion, "workspace")
}

func runCmd(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cpCmd := exec.Command(name, args...)
	cpCmd.Dir = dir
	cpOut, err := cpCmd.CombinedOutput()
	Assert(t, err == nil, "err running %q: %s", strings.Join(append([]string{name}, args...), " "), cpOut)
	return string(cpOut)
}

func initRepo(t *testing.T) string {
	repoDir := t.TempDir()
	runCmd(t, repoDir, "git", "init")
	runCmd(t, repoDir, "touch", ".gitkeep")
	runCmd(t, repoDir, "git", "add", ".gitkeep")
	runCmd(t, repoDir, "git", "config", "--local", "user.email", "atlantisbot@runatlantis.io")
	runCmd(t, repoDir, "git", "config", "--local", "user.name", "atlantisbot")
	runCmd(t, repoDir, "git", "config", "--local", "commit.gpgsign", "false")
	runCmd(t, repoDir, "git", "commit", "-m", "initial commit")
	runCmd(t, repoDir, "git", "branch", "branch")
	return repoDir
}

type restoredInitExecutor struct {
	calls int
	args  []string
}

func (e *restoredInitExecutor) EnsureVersion(logging.SimpleLogging, tf.Distribution, *version.Version) error {
	return nil
}

func (e *restoredInitExecutor) RunCommandWithVersion(_ command.ProjectContext, _ string, args []string, _ map[string]string, _ tf.Distribution, _ *version.Version, _ string) (string, error) {
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
			runner := &runtime.InitStepRunner{TerraformExecutor: executor, DefaultTFDistribution: tf.NewDistribution(tc.tool), DefaultTFVersion: version.Must(version.NewVersion(tc.version))}
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
			runner := &runtime.InitStepRunner{TerraformExecutor: executor, DefaultTFDistribution: tf.NewDistributionTerraform(), DefaultTFVersion: version.Must(version.NewVersion("1.1.0"))}
			ctx := command.ProjectContext{Log: logging.NewNoopLogger(t), RestoredDataDirectory: ".terraform"}
			if tc.missing {
				ctx.RestoredLockHash = fmt.Sprintf("%x", sha256.Sum256([]byte("locked")))
			}
			_, err := runner.Run(ctx, tc.args, path, tc.envs)
			Assert(t, err != nil, "unsafe restored initialization was accepted")
			Equals(t, 0, executor.calls)
		})
	}
}

func TestRestoredInitAllowsBackendValuesContainingFlagNames(t *testing.T) {
	path := t.TempDir()
	Ok(t, os.WriteFile(filepath.Join(path, ".terraform.lock.hcl"), []byte("locked"), 0600))
	executor := &restoredInitExecutor{}
	runner := &runtime.InitStepRunner{TerraformExecutor: executor, DefaultTFDistribution: tf.NewDistributionTerraform(), DefaultTFVersion: version.Must(version.NewVersion("1.1.0"))}
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
			runner := &runtime.InitStepRunner{TerraformExecutor: executor, DefaultTFDistribution: tf.NewDistributionTerraform(), DefaultTFVersion: version.Must(version.NewVersion(versionString))}
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

func TestRestoredInitWithoutExternalProviders(t *testing.T) {
	path := t.TempDir()
	executor := &restoredInitExecutor{}
	runner := &runtime.InitStepRunner{TerraformExecutor: executor, DefaultTFDistribution: tf.NewDistributionTerraform(), DefaultTFVersion: version.Must(version.NewVersion("1.1.0"))}
	_, err := runner.Run(command.ProjectContext{Log: logging.NewNoopLogger(t), RestoredDataDirectory: "data", Workspace: "default"}, nil, path, map[string]string{"TF_DATA_DIR": "data"})
	Ok(t, err)
	Equals(t, []string{"init", "-input=false", "-lockfile=readonly"}, executor.args)
	Equals(t, 1, executor.calls)
}
