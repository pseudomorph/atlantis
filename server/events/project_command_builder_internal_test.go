// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	version "github.com/hashicorp/go-version"
	. "github.com/petergtz/pegomock/v4"
	"github.com/runatlantis/atlantis/server/core/config"
	"github.com/runatlantis/atlantis/server/core/config/valid"
	"github.com/runatlantis/atlantis/server/core/runtime"
	tfclientmocks "github.com/runatlantis/atlantis/server/core/terraform/tfclient/mocks"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/events/vcs"
	vcsmocks "github.com/runatlantis/atlantis/server/events/vcs/mocks"
	"github.com/runatlantis/atlantis/server/logging"
	"github.com/runatlantis/atlantis/server/metrics/metricstest"
	. "github.com/runatlantis/atlantis/testing"
)

// Test different permutations of global and repo config.
func TestBuildProjectCmdCtx(t *testing.T) {
	logger := logging.NewNoopLogger(t)
	statsScope := metricstest.NewLoggingScope(t, logging.NewNoopLogger(t), "atlantis")
	emptyPolicySets := valid.PolicySets{
		Version:    nil,
		PolicySets: []valid.PolicySet{},
	}
	baseRepo := models.Repo{
		FullName: "owner/repo",
		VCSHost: models.VCSHost{
			Hostname: "github.com",
		},
	}
	pull := models.PullRequest{
		BaseRepo: baseRepo,
	}
	cases := map[string]struct {
		globalCfg     string
		repoCfg       string
		expErr        string
		expCtx        command.ProjectContext
		expPlanSteps  []string
		expApplySteps []string
	}{
		// Test that if we've set global defaults and no project config
		// that the global defaults are used.
		"global defaults": {
			globalCfg: `
repos:
- id: /.*/
  workflow: default
workflows:
  default:
    plan:
      steps:
      - init
      - plan
    apply:
      steps:
      - apply`,
			repoCfg: "",
			expCtx: command.ProjectContext{
				ApplyCmd:             "atlantis apply -d project1 -w myworkspace",
				ApprovePoliciesCmd:   "atlantis approve_policies -d project1 -w myworkspace",
				BaseRepo:             baseRepo,
				CommentArgs:          []string{"flag"},
				EscapedCommentArgs:   []string{`\f\l\a\g`},
				AutomergeEnabled:     false,
				AutoplanEnabled:      true,
				AutoplanWhenModified: []string{},
				HeadRepo:             models.Repo{},
				Log:                  logger,
				Scope:                statsScope,
				PullReqStatus: models.PullReqStatus{
					MergeableStatus: models.MergeableStatus{IsMergeable: true},
				},
				Pull:               pull,
				ProjectName:        "",
				PlanRequirements:   []string{},
				ApplyRequirements:  []string{},
				ImportRequirements: []string{},
				RePlanCmd:          "atlantis plan -d project1 -w myworkspace -- flag",
				RepoRelDir:         "project1",
				User:               models.User{},
				Verbose:            true,
				Workspace:          "myworkspace",
				PolicySets:         emptyPolicySets,
				RepoLocksMode:      valid.DefaultRepoLocksMode,
			},
			expPlanSteps:  []string{"init", "plan"},
			expApplySteps: []string{"apply"},
		},

		// Test that if we've set global defaults, that they are used but the
		// allowed project config values also come through.
		"global defaults with repo cfg": {
			globalCfg: `
repos:
- id: /.*/
  workflow: default
workflows:
  default:
    plan:
      steps:
      - init
      - plan
    apply:
      steps:
      - apply`,
			repoCfg: `
version: 3
automerge: true
projects:
- dir: project1
  workspace: myworkspace
  autoplan:
    enabled: true
    when_modified: [../modules/**/*.tf]
  terraform_version: v10.0
  `,
			expCtx: command.ProjectContext{
				ApplyCmd:             "atlantis apply -d project1 -w myworkspace",
				ApprovePoliciesCmd:   "atlantis approve_policies -d project1 -w myworkspace",
				BaseRepo:             baseRepo,
				CommentArgs:          []string{"flag"},
				EscapedCommentArgs:   []string{`\f\l\a\g`},
				AutomergeEnabled:     true,
				AutoplanEnabled:      true,
				AutoplanWhenModified: []string{"../modules/**/*.tf"},
				HeadRepo:             models.Repo{},
				Log:                  logger,
				Scope:                statsScope,
				PullReqStatus: models.PullReqStatus{
					MergeableStatus: models.MergeableStatus{IsMergeable: true},
				},
				Pull:               pull,
				ProjectName:        "",
				PlanRequirements:   []string{},
				ApplyRequirements:  []string{},
				ImportRequirements: []string{},
				RepoConfigVersion:  3,
				RePlanCmd:          "atlantis plan -d project1 -w myworkspace -- flag",
				RepoRelDir:         "project1",
				TerraformVersion:   mustVersion("10.0"),
				User:               models.User{},
				Verbose:            true,
				Workspace:          "myworkspace",
				PolicySets:         emptyPolicySets,
				RepoLocksMode:      valid.DefaultRepoLocksMode,
			},
			expPlanSteps:  []string{"init", "plan"},
			expApplySteps: []string{"apply"},
		},

		// Set a global apply req that should be used.
		"global requirements": {
			globalCfg: `
repos:
- id: /.*/
  workflow: default
  plan_requirements: [approved, mergeable]
  apply_requirements: [approved, mergeable]
  import_requirements: [approved, mergeable]
workflows:
  default:
    plan:
      steps:
      - init
      - plan
    apply:
      steps:
      - apply`,
			repoCfg: `
version: 3
automerge: true
projects:
- dir: project1
  workspace: myworkspace
  autoplan:
    enabled: true
    when_modified: [../modules/**/*.tf]
  terraform_version: v10.0
`,
			expCtx: command.ProjectContext{
				ApplyCmd:             "atlantis apply -d project1 -w myworkspace",
				ApprovePoliciesCmd:   "atlantis approve_policies -d project1 -w myworkspace",
				BaseRepo:             baseRepo,
				CommentArgs:          []string{"flag"},
				EscapedCommentArgs:   []string{`\f\l\a\g`},
				AutomergeEnabled:     true,
				AutoplanEnabled:      true,
				AutoplanWhenModified: []string{"../modules/**/*.tf"},
				HeadRepo:             models.Repo{},
				Log:                  logger,
				Scope:                statsScope,
				PullReqStatus: models.PullReqStatus{
					MergeableStatus: models.MergeableStatus{IsMergeable: true},
				},
				Pull:               pull,
				ProjectName:        "",
				PlanRequirements:   []string{"approved", "mergeable"},
				ApplyRequirements:  []string{"approved", "mergeable"},
				ImportRequirements: []string{"approved", "mergeable"},
				RepoConfigVersion:  3,
				RePlanCmd:          "atlantis plan -d project1 -w myworkspace -- flag",
				RepoRelDir:         "project1",
				TerraformVersion:   mustVersion("10.0"),
				User:               models.User{},
				Verbose:            true,
				Workspace:          "myworkspace",
				PolicySets:         emptyPolicySets,
				RepoLocksMode:      valid.DefaultRepoLocksMode,
			},
			expPlanSteps:  []string{"init", "plan"},
			expApplySteps: []string{"apply"},
		},

		// If we have global config that matches a specific repo, it should be used.
		"specific repo": {
			globalCfg: `
repos:
- id: /.*/
  workflow: default
- id: github.com/owner/repo
  workflow: specific
  plan_requirements: [approved]
  apply_requirements: [approved]
  import_requirements: [approved]
workflows:
  default:
    plan:
      steps:
      - init
      - plan
    apply:
      steps:
      - apply
  specific:
    plan:
      steps:
      - plan
    apply:
      steps: []`,
			repoCfg: `
version: 3
automerge: true
projects:
- dir: project1
  workspace: myworkspace
  autoplan:
    enabled: true
    when_modified: [../modules/**/*.tf]
  terraform_version: v10.0
`,
			expCtx: command.ProjectContext{
				ApplyCmd:             "atlantis apply -d project1 -w myworkspace",
				ApprovePoliciesCmd:   "atlantis approve_policies -d project1 -w myworkspace",
				BaseRepo:             baseRepo,
				CommentArgs:          []string{"flag"},
				EscapedCommentArgs:   []string{`\f\l\a\g`},
				AutomergeEnabled:     true,
				AutoplanEnabled:      true,
				AutoplanWhenModified: []string{"../modules/**/*.tf"},
				HeadRepo:             models.Repo{},
				Log:                  logger,
				Scope:                statsScope,
				PullReqStatus: models.PullReqStatus{
					MergeableStatus: models.MergeableStatus{IsMergeable: true},
				},
				Pull:               pull,
				ProjectName:        "",
				PlanRequirements:   []string{"approved"},
				ApplyRequirements:  []string{"approved"},
				ImportRequirements: []string{"approved"},
				RepoConfigVersion:  3,
				RePlanCmd:          "atlantis plan -d project1 -w myworkspace -- flag",
				RepoRelDir:         "project1",
				TerraformVersion:   mustVersion("10.0"),
				User:               models.User{},
				Verbose:            true,
				Workspace:          "myworkspace",
				PolicySets:         emptyPolicySets,
				RepoLocksMode:      valid.DefaultRepoLocksMode,
			},
			expPlanSteps:  []string{"plan"},
			expApplySteps: []string{},
		},

		// We should get an error if the repo sets an apply req when its
		// not allowed.
		"repo defines apply_requirements": {
			globalCfg: `
repos:
- id: /.*/
  workflow: default
  apply_requirements: [approved, mergeable]
workflows:
  default:
    plan:
      steps:
      - init
      - plan
    apply:
      steps:
      - apply`,
			repoCfg: `
version: 3
automerge: true
projects:
- dir: project1
  workspace: myworkspace
  apply_requirements: []
`,
			expErr: "repo config not allowed to set 'apply_requirements' key: server-side config needs 'allowed_overrides: [apply_requirements]'",
		},

		// We should get an error if a repo sets a workflow when it's not allowed.
		"repo sets its own workflow": {
			globalCfg: `
repos:
- id: /.*/
  workflow: default
  apply_requirements: [approved, mergeable]
workflows:
  default:
    plan:
      steps:
      - init
      - plan
    apply:
      steps:
      - apply`,
			repoCfg: `
version: 3
automerge: true
projects:
- dir: project1
  workspace: myworkspace
  workflow: default
`,
			expErr: "repo config not allowed to set 'workflow' key: server-side config needs 'allowed_overrides: [workflow]'",
		},

		// We should get an error if a repo defines a workflow when it's not
		// allowed.
		"repo defines new workflow": {
			globalCfg: `
repos:
- id: /.*/
  workflow: default
  apply_requirements: [approved, mergeable]
workflows:
  default:
    plan:
      steps:
      - init
      - plan
    apply:
      steps:
      - apply`,
			repoCfg: `
version: 3
automerge: true
projects:
- dir: project1
  workspace: myworkspace
workflows:
  new: ~
`,
			expErr: "repo config not allowed to define custom workflows: server-side config needs 'allow_custom_workflows: true'",
		},

		// If the repos are allowed to set everything then their config should
		// come through.
		"full repo permissions": {
			globalCfg: `
repos:
- id: /.*/
  workflow: default
  apply_requirements: [approved]
  import_requirements: [approved]
  allowed_overrides: [apply_requirements, import_requirements, workflow]
  allow_custom_workflows: true
workflows:
  default:
    plan:
      steps: []
    apply:
      steps: []
`,
			repoCfg: `
version: 3
automerge: true
projects:
- dir: project1
  workspace: myworkspace
  autoplan:
    enabled: true
    when_modified: [../modules/**/*.tf]
  terraform_version: v10.0
  apply_requirements: []
  import_requirements: []
  workflow: custom
workflows:
  custom:
    plan:
      steps:
      - plan
    apply:
      steps:
      - apply
`,
			expCtx: command.ProjectContext{
				ApplyCmd:             "atlantis apply -d project1 -w myworkspace",
				ApprovePoliciesCmd:   "atlantis approve_policies -d project1 -w myworkspace",
				BaseRepo:             baseRepo,
				CommentArgs:          []string{"flag"},
				EscapedCommentArgs:   []string{`\f\l\a\g`},
				AutomergeEnabled:     true,
				AutoplanEnabled:      true,
				AutoplanWhenModified: []string{"../modules/**/*.tf"},
				HeadRepo:             models.Repo{},
				Log:                  logger,
				Scope:                statsScope,
				PullReqStatus: models.PullReqStatus{
					MergeableStatus: models.MergeableStatus{IsMergeable: true},
				},
				Pull:               pull,
				ProjectName:        "",
				PlanRequirements:   []string{},
				ApplyRequirements:  []string{},
				ImportRequirements: []string{},
				RepoConfigVersion:  3,
				RePlanCmd:          "atlantis plan -d project1 -w myworkspace -- flag",
				RepoRelDir:         "project1",
				TerraformVersion:   mustVersion("10.0"),
				User:               models.User{},
				Verbose:            true,
				Workspace:          "myworkspace",
				PolicySets:         emptyPolicySets,
				RepoLocksMode:      valid.DefaultRepoLocksMode,
			},
			expPlanSteps:  []string{"plan"},
			expApplySteps: []string{"apply"},
		},

		// Repos can choose server-side workflows.
		"repos choose server-side workflow": {
			globalCfg: `
repos:
- id: /.*/
  workflow: default
  allowed_overrides: [workflow]
workflows:
  default:
    plan:
      steps: []
    apply:
      steps: []
  custom:
    plan:
      steps: [plan]
    apply:
      steps: [apply]
`,
			repoCfg: `
version: 3
automerge: true
projects:
- dir: project1
  workspace: myworkspace
  autoplan:
    enabled: true
    when_modified: [../modules/**/*.tf]
  terraform_version: v10.0
  workflow: custom
`,
			expCtx: command.ProjectContext{
				ApplyCmd:             "atlantis apply -d project1 -w myworkspace",
				ApprovePoliciesCmd:   "atlantis approve_policies -d project1 -w myworkspace",
				BaseRepo:             baseRepo,
				CommentArgs:          []string{"flag"},
				EscapedCommentArgs:   []string{`\f\l\a\g`},
				AutomergeEnabled:     true,
				AutoplanEnabled:      true,
				AutoplanWhenModified: []string{"../modules/**/*.tf"},
				HeadRepo:             models.Repo{},
				Log:                  logger,
				Scope:                statsScope,
				PullReqStatus: models.PullReqStatus{
					MergeableStatus: models.MergeableStatus{IsMergeable: true},
				},
				Pull:               pull,
				ProjectName:        "",
				PlanRequirements:   []string{},
				ApplyRequirements:  []string{},
				ImportRequirements: []string{},
				RepoConfigVersion:  3,
				RePlanCmd:          "atlantis plan -d project1 -w myworkspace -- flag",
				RepoRelDir:         "project1",
				TerraformVersion:   mustVersion("10.0"),
				User:               models.User{},
				Verbose:            true,
				Workspace:          "myworkspace",
				PolicySets:         emptyPolicySets,
				RepoLocksMode:      valid.DefaultRepoLocksMode,
			},
			expPlanSteps:  []string{"plan"},
			expApplySteps: []string{"apply"},
		},

		// Repo-side workflows with the same name override server-side if
		// allowed.
		"repo-side workflow override": {
			globalCfg: `
repos:
- id: /.*/
  workflow: custom
  allowed_overrides: [workflow]
  allow_custom_workflows: true
workflows:
  custom:
    plan:
      steps: [plan]
    apply:
      steps: [apply]
`,
			repoCfg: `
version: 3
automerge: true
projects:
- dir: project1
  workspace: myworkspace
  autoplan:
    enabled: true
    when_modified: [../modules/**/*.tf]
  terraform_version: v10.0
  workflow: custom
workflows:
  custom:
    plan:
      steps: []
    apply:
      steps: []
`,
			expCtx: command.ProjectContext{
				ApplyCmd:             "atlantis apply -d project1 -w myworkspace",
				ApprovePoliciesCmd:   "atlantis approve_policies -d project1 -w myworkspace",
				BaseRepo:             baseRepo,
				CommentArgs:          []string{"flag"},
				EscapedCommentArgs:   []string{`\f\l\a\g`},
				AutomergeEnabled:     true,
				AutoplanEnabled:      true,
				AutoplanWhenModified: []string{"../modules/**/*.tf"},
				HeadRepo:             models.Repo{},
				Log:                  logger,
				Scope:                statsScope,
				PullReqStatus: models.PullReqStatus{
					MergeableStatus: models.MergeableStatus{IsMergeable: true},
				},
				Pull:               pull,
				ProjectName:        "",
				PlanRequirements:   []string{},
				ApplyRequirements:  []string{},
				ImportRequirements: []string{},
				RepoConfigVersion:  3,
				RePlanCmd:          "atlantis plan -d project1 -w myworkspace -- flag",
				RepoRelDir:         "project1",
				TerraformVersion:   mustVersion("10.0"),
				User:               models.User{},
				Verbose:            true,
				Workspace:          "myworkspace",
				PolicySets:         emptyPolicySets,
				RepoLocksMode:      valid.DefaultRepoLocksMode,
			},
			expPlanSteps:  []string{},
			expApplySteps: []string{},
		},
		// Test that if we leave keys undefined, that they don't override.
		"cascading matches": {
			globalCfg: `
repos:
- id: /.*/
  plan_requirements: [approved]
  apply_requirements: [approved]
  import_requirements: [approved]
- id: github.com/owner/repo
  workflow: custom
workflows:
  custom:
    plan:
      steps: [plan]
`,
			repoCfg: `
version: 3
projects:
- dir: project1
  workspace: myworkspace
`,
			expCtx: command.ProjectContext{
				ApplyCmd:             "atlantis apply -d project1 -w myworkspace",
				ApprovePoliciesCmd:   "atlantis approve_policies -d project1 -w myworkspace",
				BaseRepo:             baseRepo,
				CommentArgs:          []string{"flag"},
				EscapedCommentArgs:   []string{`\f\l\a\g`},
				AutomergeEnabled:     false,
				AutoplanEnabled:      true,
				AutoplanWhenModified: []string{"**/*.tf*", "**/*.tofu", "**/*.tofu.json", "**/terragrunt.hcl", "**/.terraform.lock.hcl"},
				HeadRepo:             models.Repo{},
				Log:                  logger,
				Scope:                statsScope,
				PullReqStatus: models.PullReqStatus{
					MergeableStatus: models.MergeableStatus{IsMergeable: true},
				},
				Pull:               pull,
				ProjectName:        "",
				PlanRequirements:   []string{"approved"},
				ApplyRequirements:  []string{"approved"},
				ImportRequirements: []string{"approved"},
				RepoConfigVersion:  3,
				RePlanCmd:          "atlantis plan -d project1 -w myworkspace -- flag",
				RepoRelDir:         "project1",
				User:               models.User{},
				Verbose:            true,
				Workspace:          "myworkspace",
				PolicySets:         emptyPolicySets,
				RepoLocksMode:      valid.DefaultRepoLocksMode,
			},
			expPlanSteps:  []string{"plan"},
			expApplySteps: []string{"apply"},
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tmp := DirStructure(t, map[string]any{
				"project1": map[string]any{
					"main.tf": nil,
				},
				"modules": map[string]any{
					"module": map[string]any{
						"main.tf": nil,
					},
				},
			})

			workingDir := NewMockWorkingDir()
			When(workingDir.Clone(Any[logging.SimpleLogging](), Any[models.Repo](), Any[models.PullRequest](),
				Any[string]())).ThenReturn(tmp, nil)
			vcsClient := vcsmocks.NewMockClient()
			When(vcsClient.GetModifiedFiles(Any[logging.SimpleLogging](), Any[models.Repo](),
				Any[models.PullRequest]())).ThenReturn([]string{"modules/module/main.tf"}, nil)

			// Write and parse the global config file.
			globalCfgPath := filepath.Join(tmp, "global.yaml")
			Ok(t, os.WriteFile(globalCfgPath, []byte(c.globalCfg), 0600))
			parser := &config.ParserValidator{}
			globalCfgArgs := valid.GlobalCfgArgs{}
			globalCfg, err := parser.ParseGlobalCfg(globalCfgPath, valid.NewGlobalCfgFromArgs(globalCfgArgs))
			Ok(t, err)

			if c.repoCfg != "" {
				Ok(t, os.WriteFile(filepath.Join(tmp, "atlantis.yaml"), []byte(c.repoCfg), 0600))
			}

			terraformClient := tfclientmocks.NewMockClient()

			builder := NewProjectCommandBuilder(
				false,
				parser,
				&DefaultProjectFinder{},
				vcsClient,
				workingDir,
				NewDefaultWorkingDirLocker(),
				globalCfg,
				&DefaultPendingPlanFinder{},
				&CommentParser{ExecutableName: "atlantis"},
				false,
				false,
				false,
				false,
				false,
				"",
				"**/*.tf,**/*.tfvars,**/*.tfvars.json,**/terragrunt.hcl,**/.terraform.lock.hcl",
				false,
				"",
				false,
				false,
				"auto",
				statsScope,
				terraformClient,
				&runtime.LocalPlanStore{},
			)

			// We run a test for each type of command.
			for _, cmd := range []command.Name{command.Plan, command.Apply} {
				t.Run(cmd.String(), func(t *testing.T) {
					ctxs, err := builder.buildProjectCommandCtx(&command.Context{
						Log:   logger,
						Scope: statsScope,
						Pull: models.PullRequest{
							BaseRepo: baseRepo,
						},
						PullRequestStatus: models.PullReqStatus{
							MergeableStatus: models.MergeableStatus{IsMergeable: true},
						},
					}, cmd, "", "", []string{"flag"}, tmp, "project1", "myworkspace", false, false, true)

					if c.expErr != "" {
						ErrEquals(t, c.expErr, err)
						return
					}

					Ok(t, err)
					ctx := ctxs[0]

					// Construct expected steps.
					var stepNames []string
					switch cmd {
					case command.Plan:
						stepNames = c.expPlanSteps
					case command.Apply:
						stepNames = c.expApplySteps
					}
					var expSteps []valid.Step
					for _, stepName := range stepNames {
						expSteps = append(expSteps, valid.Step{
							StepName: stepName,
						})
					}

					c.expCtx.CommandName = cmd
					// Init fields we couldn't in our cases map.
					c.expCtx.Steps = expSteps
					// Atlantis owns the convention plan artifact only when the
					// workflow uses the built-in plan or apply step.
					c.expCtx.RequiresAtlantisManagedPlanFile = slices.Contains(c.expPlanSteps, "plan") ||
						slices.Contains(c.expApplySteps, "apply")
					ctx.PolicySets = emptyPolicySets

					// Job ID cannot be compared since its generated at random
					ctx.JobID = ""

					Equals(t, c.expCtx, ctx)
					// Equals() doesn't compare TF version properly so have to
					// use .String().
					if c.expCtx.TerraformVersion != nil {
						Equals(t, c.expCtx.TerraformVersion.String(), ctx.TerraformVersion.String())
					}
				})
			}
		})
	}
}

func TestBuildProjectCmdCtx_WithRegExpCmdEnabled(t *testing.T) {
	statsScope := metricstest.NewLoggingScope(t, logging.NewNoopLogger(t), "atlantis")
	emptyPolicySets := valid.PolicySets{
		Version:    nil,
		PolicySets: []valid.PolicySet{},
	}
	baseRepo := models.Repo{
		FullName: "owner/repo",
		VCSHost: models.VCSHost{
			Hostname: "github.com",
		},
	}
	pull := models.PullRequest{
		BaseRepo: baseRepo,
	}
	cases := map[string]struct {
		globalCfg     string
		repoCfg       string
		expErr        string
		expCtx        command.ProjectContext
		expPlanSteps  []string
		expApplySteps []string
	}{

		// Test that if we've set global defaults, that they are used but the
		// allowed project config values also come through.
		"global defaults with repo cfg": {
			globalCfg: `
repos:
- id: /.*/
  workflow: default
workflows:
  default:
    plan:
      steps:
      - init
      - plan
    apply:
      steps:
      - apply`,
			repoCfg: `
version: 3
automerge: true
projects:
- name: myproject_1
  dir: project1
  workspace: myworkspace
  autoplan:
    enabled: true
    when_modified: [../modules/**/*.tf]
  terraform_version: v10.0
- name: myproject_2
  dir: project2
  workspace: myworkspace
  autoplan:
    enabled: true
    when_modified: [../modules/**/*.tf]
  terraform_version: v10.0
- name: myproject_3
  dir: project3
  workspace: myworkspace
  autoplan:
    enabled: true
    when_modified: [../modules/**/*.tf]
  terraform_version: v10.0
  `,
			expCtx: command.ProjectContext{
				ApplyCmd:             "atlantis apply -p myproject_1",
				ApprovePoliciesCmd:   "atlantis approve_policies -p myproject_1",
				BaseRepo:             baseRepo,
				CommentArgs:          []string{"flag"},
				EscapedCommentArgs:   []string{`\f\l\a\g`},
				AutomergeEnabled:     true,
				AutoplanEnabled:      true,
				AutoplanWhenModified: []string{"../modules/**/*.tf"},
				HeadRepo:             models.Repo{},
				Log:                  logging.NewNoopLogger(t),
				Scope:                statsScope,
				PullReqStatus: models.PullReqStatus{
					MergeableStatus: models.MergeableStatus{IsMergeable: true},
				},
				Pull:               pull,
				ProjectName:        "myproject_1",
				PlanRequirements:   []string{},
				ApplyRequirements:  []string{},
				ImportRequirements: []string{},
				RepoConfigVersion:  3,
				RePlanCmd:          "atlantis plan -p myproject_1 -- flag",
				RepoRelDir:         "project1",
				TerraformVersion:   mustVersion("10.0"),
				User:               models.User{},
				Verbose:            true,
				Workspace:          "myworkspace",
				PolicySets:         emptyPolicySets,
				RepoLocksMode:      valid.DefaultRepoLocksMode,
			},
			expPlanSteps:  []string{"init", "plan"},
			expApplySteps: []string{"apply"},
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tmp := DirStructure(t, map[string]any{
				"project1": map[string]any{
					"main.tf": nil,
				},
				"modules": map[string]any{
					"module": map[string]any{
						"main.tf": nil,
					},
				},
			})

			workingDir := NewMockWorkingDir()
			When(workingDir.Clone(Any[logging.SimpleLogging](), Any[models.Repo](), Any[models.PullRequest](),
				Any[string]())).ThenReturn(tmp, nil)
			vcsClient := vcsmocks.NewMockClient()
			When(vcsClient.GetModifiedFiles(Any[logging.SimpleLogging](), Any[models.Repo](),
				Any[models.PullRequest]())).ThenReturn([]string{"modules/module/main.tf"}, nil)

			// Write and parse the global config file.
			globalCfgPath := filepath.Join(tmp, "global.yaml")
			Ok(t, os.WriteFile(globalCfgPath, []byte(c.globalCfg), 0600))
			parser := &config.ParserValidator{}
			globalCfgArgs := valid.GlobalCfgArgs{}
			globalCfg, err := parser.ParseGlobalCfg(globalCfgPath, valid.NewGlobalCfgFromArgs(globalCfgArgs))
			Ok(t, err)

			if c.repoCfg != "" {
				Ok(t, os.WriteFile(filepath.Join(tmp, "atlantis.yaml"), []byte(c.repoCfg), 0600))
			}

			statsScope := metricstest.NewLoggingScope(t, logging.NewNoopLogger(t), "atlantis")

			terraformClient := tfclientmocks.NewMockClient()

			builder := NewProjectCommandBuilder(
				false,
				parser,
				&DefaultProjectFinder{},
				vcsClient,
				workingDir,
				NewDefaultWorkingDirLocker(),
				globalCfg,
				&DefaultPendingPlanFinder{},
				&CommentParser{ExecutableName: "atlantis"},
				false,
				true,
				false,
				false,
				false,
				"",
				"**/*.tf,**/*.tfvars,**/*.tfvars.json,**/terragrunt.hcl,**/.terraform.lock.hcl",
				false,
				"",
				false,
				false,
				"auto",
				statsScope,
				terraformClient,
				&runtime.LocalPlanStore{},
			)

			// We run a test for each type of command, again specific projects
			for _, cmd := range []command.Name{command.Plan, command.Apply} {
				t.Run(cmd.String(), func(t *testing.T) {
					ctxs, err := builder.buildProjectCommandCtx(&command.Context{
						Pull: models.PullRequest{
							BaseRepo: baseRepo,
						},
						Log:   logging.NewNoopLogger(t),
						Scope: statsScope,
						PullRequestStatus: models.PullReqStatus{
							MergeableStatus: models.MergeableStatus{IsMergeable: true},
						},
					}, cmd, "", "myproject_[1-2]", []string{"flag"}, tmp, "project1", "myworkspace", false, false, true)

					if c.expErr != "" {
						ErrEquals(t, c.expErr, err)
						return
					}

					Ok(t, err)
					ctx := ctxs[0]

					Equals(t, 2, len(ctxs))
					// Construct expected steps.
					var stepNames []string
					switch cmd {
					case command.Plan:
						stepNames = c.expPlanSteps
					case command.Apply:
						stepNames = c.expApplySteps
					}
					var expSteps []valid.Step
					for _, stepName := range stepNames {
						expSteps = append(expSteps, valid.Step{
							StepName: stepName,
						})
					}

					c.expCtx.CommandName = cmd
					// Init fields we couldn't in our cases map.
					c.expCtx.Steps = expSteps
					// Atlantis owns the convention plan artifact only when the
					// workflow uses the built-in plan or apply step.
					c.expCtx.RequiresAtlantisManagedPlanFile = slices.Contains(c.expPlanSteps, "plan") ||
						slices.Contains(c.expApplySteps, "apply")
					ctx.PolicySets = emptyPolicySets

					// Job ID cannot be compared since its generated at random
					ctx.JobID = ""

					Equals(t, c.expCtx, ctx)
					// Equals() doesn't compare TF version properly so have to
					// use .String().
					if c.expCtx.TerraformVersion != nil {
						Equals(t, c.expCtx.TerraformVersion.String(), ctx.TerraformVersion.String())
					}
				})
			}
		})
	}
}

func TestBuildProjectCmdCtx_WithPolicCheckEnabled(t *testing.T) {
	logger := logging.NewNoopLogger(t)
	statsScope := metricstest.NewLoggingScope(t, logging.NewNoopLogger(t), "atlantis")
	emptyPolicySets := valid.PolicySets{
		Version:    nil,
		PolicySets: []valid.PolicySet{},
	}
	baseRepo := models.Repo{
		FullName: "owner/repo",
		VCSHost: models.VCSHost{
			Hostname: "github.com",
		},
	}
	pull := models.PullRequest{
		BaseRepo: baseRepo,
	}
	cases := map[string]struct {
		globalCfg           string
		repoCfg             string
		expErr              string
		expCtx              command.ProjectContext
		expPolicyCheckSteps []string
	}{
		// Test that if we've set global defaults and no project config
		// that the global defaults are used.
		"global defaults": {
			globalCfg: `
repos:
- id: /.*/
`,
			repoCfg: "",
			expCtx: command.ProjectContext{
				ApplyCmd:             "atlantis apply -d project1 -w myworkspace",
				ApprovePoliciesCmd:   "atlantis approve_policies -d project1 -w myworkspace",
				BaseRepo:             baseRepo,
				CommentArgs:          []string{"flag"},
				EscapedCommentArgs:   []string{`\f\l\a\g`},
				AutomergeEnabled:     false,
				AutoplanEnabled:      true,
				AutoplanWhenModified: []string{},
				HeadRepo:             models.Repo{},
				Log:                  logger,
				Scope:                statsScope,
				PullReqStatus: models.PullReqStatus{
					MergeableStatus: models.MergeableStatus{IsMergeable: true},
				},
				Pull:               pull,
				ProjectName:        "",
				PlanRequirements:   []string{},
				ApplyRequirements:  []string{"policies_passed"},
				ImportRequirements: []string{},
				RePlanCmd:          "atlantis plan -d project1 -w myworkspace -- flag",
				RepoRelDir:         "project1",
				User:               models.User{},
				Verbose:            true,
				Workspace:          "myworkspace",
				PolicySets:         emptyPolicySets,
				RepoLocksMode:      valid.DefaultRepoLocksMode,
			},
			expPolicyCheckSteps: []string{"show", "policy_check"},
		},

		// If the repos are allowed to set everything then their config should
		// come through.
		"full repo permissions": {
			globalCfg: `
repos:
- id: /.*/
  workflow: default
  apply_requirements: [approved]
  allowed_overrides: [apply_requirements, workflow]
  allow_custom_workflows: true
workflows:
  default:
    policy_check:
      steps: []
`,
			repoCfg: `
version: 3
automerge: true
projects:
- dir: project1
  workspace: myworkspace
  autoplan:
    enabled: true
    when_modified: [../modules/**/*.tf]
  terraform_version: v10.0
  apply_requirements: []
  workflow: custom
workflows:
  custom:
    policy_check:
      steps:
      - policy_check
`,
			expCtx: command.ProjectContext{
				ApplyCmd:             "atlantis apply -d project1 -w myworkspace",
				ApprovePoliciesCmd:   "atlantis approve_policies -d project1 -w myworkspace",
				BaseRepo:             baseRepo,
				CommentArgs:          []string{"flag"},
				EscapedCommentArgs:   []string{`\f\l\a\g`},
				AutomergeEnabled:     true,
				AutoplanEnabled:      true,
				AutoplanWhenModified: []string{"../modules/**/*.tf"},
				HeadRepo:             models.Repo{},
				Log:                  logger,
				Scope:                statsScope,
				PullReqStatus: models.PullReqStatus{
					MergeableStatus: models.MergeableStatus{IsMergeable: true},
				},
				Pull:               pull,
				ProjectName:        "",
				PlanRequirements:   []string{},
				ApplyRequirements:  []string{"policies_passed"},
				ImportRequirements: []string{},
				RepoConfigVersion:  3,
				RePlanCmd:          "atlantis plan -d project1 -w myworkspace -- flag",
				RepoRelDir:         "project1",
				TerraformVersion:   mustVersion("v10.0"),
				User:               models.User{},
				Verbose:            true,
				Workspace:          "myworkspace",
				PolicySets:         emptyPolicySets,
				RepoLocksMode:      valid.DefaultRepoLocksMode,
				PolicySetTarget:    "",
			},
			expPolicyCheckSteps: []string{"policy_check"},
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tmp := DirStructure(t, map[string]any{
				"project1": map[string]any{
					"main.tf": nil,
				},
				"modules": map[string]any{
					"module": map[string]any{
						"main.tf": nil,
					},
				},
			})

			workingDir := NewMockWorkingDir()
			When(workingDir.Clone(Any[logging.SimpleLogging](), Any[models.Repo](), Any[models.PullRequest](),
				Any[string]())).ThenReturn(tmp, nil)
			vcsClient := vcsmocks.NewMockClient()
			When(vcsClient.GetModifiedFiles(Any[logging.SimpleLogging](), Any[models.Repo](),
				Any[models.PullRequest]())).ThenReturn([]string{"modules/module/main.tf"}, nil)

			// Write and parse the global config file.
			globalCfgPath := filepath.Join(tmp, "global.yaml")
			Ok(t, os.WriteFile(globalCfgPath, []byte(c.globalCfg), 0600))
			parser := &config.ParserValidator{}
			globalCfgArgs := valid.GlobalCfgArgs{
				PolicyCheckEnabled: true,
			}

			globalCfg, err := parser.ParseGlobalCfg(globalCfgPath, valid.NewGlobalCfgFromArgs(globalCfgArgs))
			Ok(t, err)

			if c.repoCfg != "" {
				Ok(t, os.WriteFile(filepath.Join(tmp, "atlantis.yaml"), []byte(c.repoCfg), 0600))
			}
			statsScope := metricstest.NewLoggingScope(t, logging.NewNoopLogger(t), "atlantis")

			terraformClient := tfclientmocks.NewMockClient()

			builder := NewProjectCommandBuilder(
				true,
				parser,
				&DefaultProjectFinder{},
				vcsClient,
				workingDir,
				NewDefaultWorkingDirLocker(),
				globalCfg,
				&DefaultPendingPlanFinder{},
				&CommentParser{ExecutableName: "atlantis"},
				false,
				false,
				false,
				false,
				false,
				"",
				"**/*.tf,**/*.tfvars,**/*.tfvars.json,**/terragrunt.hcl,**/.terraform.lock.hcl",
				false,
				"",
				false,
				false,
				"auto",
				statsScope,
				terraformClient,
				&runtime.LocalPlanStore{},
			)

			cmd := command.PolicyCheck
			t.Run(cmd.String(), func(t *testing.T) {
				ctxs, err := builder.buildProjectCommandCtx(&command.Context{
					Log:   logger,
					Scope: statsScope,
					Pull: models.PullRequest{
						BaseRepo: baseRepo,
					},
					PullRequestStatus: models.PullReqStatus{
						MergeableStatus: models.MergeableStatus{IsMergeable: true},
					},
				}, command.Plan, "", "", []string{"flag"}, tmp, "project1", "myworkspace", false, false, true)

				if c.expErr != "" {
					ErrEquals(t, c.expErr, err)
					return
				}

				Ok(t, err)
				ctx := ctxs[1]

				// Construct expected steps.
				var stepNames []string
				var expSteps []valid.Step

				stepNames = c.expPolicyCheckSteps
				for _, stepName := range stepNames {
					expSteps = append(expSteps, valid.Step{
						StepName: stepName,
					})
				}

				c.expCtx.CommandName = cmd
				// Init fields we couldn't in our cases map.
				c.expCtx.Steps = expSteps
				// These cases only override policy_check, so plan and apply
				// fall back to the built-in default steps and Atlantis owns
				// the convention plan artifact.
				c.expCtx.RequiresAtlantisManagedPlanFile = true
				ctx.PolicySets = emptyPolicySets

				// Job ID cannot be compared since its generated at random
				ctx.JobID = ""

				Equals(t, c.expCtx, ctx)
				// Equals() doesn't compare TF version properly so have to
				// use .String().
				if c.expCtx.TerraformVersion != nil {
					Equals(t, c.expCtx.TerraformVersion.String(), ctx.TerraformVersion.String())
				}
			})
		})
	}
}

func TestBuildProjectCmdCtx_WithSilenceNoProjects(t *testing.T) {
	globalCfg := `
repos:
- id: /.*/
`
	logger := logging.NewNoopLogger(t)
	baseRepo := models.Repo{
		FullName: "owner/repo",
		VCSHost: models.VCSHost{
			Hostname: "github.com",
		},
	}
	cases := map[string]struct {
		repoCfg string
		expLen  int
	}{
		// One project matches the repo cfg, return it
		"matching project": {
			repoCfg: `
version: 3
automerge: true
projects:
- dir: project1
  workspace: myworkspace
`,
			expLen: 1,
		},
		// No project matches the repo cfg, ignore it
		"no matching project": {
			repoCfg: `
version: 3
automerge: true
projects:
- dir: project2
  workspace: myworkspace
`,
			expLen: 0,
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tmp := DirStructure(t, map[string]any{
				"project1": map[string]any{
					"main.tf": nil,
				},
				"modules": map[string]any{
					"module": map[string]any{
						"main.tf": nil,
					},
				},
			})

			workingDir := NewMockWorkingDir()
			When(workingDir.Clone(Any[logging.SimpleLogging](), Any[models.Repo](), Any[models.PullRequest](),
				Any[string]())).ThenReturn(tmp, nil)
			vcsClient := vcsmocks.NewMockClient()
			When(vcsClient.GetModifiedFiles(Any[logging.SimpleLogging](), Any[models.Repo](),
				Any[models.PullRequest]())).ThenReturn([]string{"modules/module/main.tf"}, nil)

			// Write and parse the global config file.
			globalCfgPath := filepath.Join(tmp, "global.yaml")
			Ok(t, os.WriteFile(globalCfgPath, []byte(globalCfg), 0600))
			parser := &config.ParserValidator{}
			globalCfgArgs := valid.GlobalCfgArgs{}

			globalCfg, err := parser.ParseGlobalCfg(globalCfgPath, valid.NewGlobalCfgFromArgs(globalCfgArgs))
			Ok(t, err)

			if c.repoCfg != "" {
				Ok(t, os.WriteFile(filepath.Join(tmp, "atlantis.yaml"), []byte(c.repoCfg), 0600))
			}
			statsScope := metricstest.NewLoggingScope(t, logging.NewNoopLogger(t), "atlantis")

			terraformClient := tfclientmocks.NewMockClient()

			builder := NewProjectCommandBuilder(
				false,
				parser,
				&DefaultProjectFinder{},
				vcsClient,
				workingDir,
				NewDefaultWorkingDirLocker(),
				globalCfg,
				&DefaultPendingPlanFinder{},
				&CommentParser{ExecutableName: "atlantis"},
				false,
				false,
				false,
				false,
				false,
				"",
				"**/*.tf,**/*.tfvars,**/*.tfvars.json,**/terragrunt.hcl,**/.terraform.lock.hcl",
				false,
				"",
				true,
				false,
				"auto",
				statsScope,
				terraformClient,
				&runtime.LocalPlanStore{},
			)

			for _, cmd := range []command.Name{command.Plan, command.Apply} {
				t.Run(cmd.String(), func(t *testing.T) {
					ctxs, err := builder.buildProjectCommandCtx(&command.Context{
						Log:   logger,
						Scope: statsScope,
						Pull: models.PullRequest{
							BaseRepo: baseRepo,
						},
						PullRequestStatus: models.PullReqStatus{
							MergeableStatus: models.MergeableStatus{IsMergeable: true},
						},
					}, cmd, "", "", []string{}, tmp, "project1", "myworkspace", false, false, true)
					Equals(t, c.expLen, len(ctxs))
					Ok(t, err)
				})
			}
		})
	}
}

func TestBuildProjectCmdCtx_AutoDiscoverRespectsRepoConfig(t *testing.T) {
	logger := logging.NewNoopLogger(t)
	cases := map[string]struct {
		globalCfg     string
		repoCfg       string
		modifiedFiles []string
		expLen        int
	}{
		"autodiscover disabled": {
			globalCfg: `
repos:
- id: /.*/
  autodiscover:
    mode: disabled
`,
			repoCfg: `
version: 3
automerge: true
`,
			modifiedFiles: []string{"project1/main.tf", "project2/main.tf", "project3/main.tf"},
			expLen:        0,
		},
		"autodiscover auto": {
			globalCfg: `
repos:
- id: /.*/
  autodiscover:
    mode: auto
`,
			repoCfg: `
version: 3
automerge: true
projects:
- dir: project1
  workspace: myworkspace
`,
			modifiedFiles: []string{"project1/main.tf", "project2/main.tf", "project3/main.tf"},
			expLen:        1,
		},
		"autodiscover enabled": {
			globalCfg: `
repos:
- id: /.*/
  autodiscover:
    mode: enabled
`,
			repoCfg: `
version: 3
automerge: true
projects:
- dir: project1
  workspace: myworkspace
`,
			modifiedFiles: []string{"project1/main.tf", "project2/main.tf", "project3/main.tf"},
			expLen:        3,
		},
		"autodiscover enabled, disabled at repo level": {
			globalCfg: `
repos:
- id: /.*/
  autodiscover:
    mode: enabled
`,
			repoCfg: `
version: 3
automerge: true
projects:
- dir: project1
  workspace: myworkspace
autodiscover:
  mode: disabled
`,
			modifiedFiles: []string{"project1/main.tf", "project2/main.tf", "project3/main.tf"},
			expLen:        3,
		},
		"autodiscover inherited from broad global config, disabled at repo level": {
			globalCfg: `
repos:
- id: /.*/
  autodiscover:
    mode: enabled
- id: /.*/
  allowed_overrides: [workflow]
`,
			repoCfg: `
version: 3
automerge: true
projects:
- dir: project1
  workspace: myworkspace
autodiscover:
  mode: disabled
`,
			modifiedFiles: []string{"project1/main.tf", "project2/main.tf", "project3/main.tf"},
			expLen:        3,
		},
		"autodiscover respects ignore_paths in repo config": {
			globalCfg: `
repos:
- id: /.*/
`,
			repoCfg: `
version: 3
automerge: true
projects:
- dir: project1
  workspace: myworkspace
autodiscover:
  mode: enabled
  ignore_paths:
  - project3
`,
			modifiedFiles: []string{"project1/main.tf", "project2/main.tf", "project3/main.tf"},
			expLen:        2,
		},
		"autodiscover respects ignore_paths in global config": {
			globalCfg: `
repos:
- id: /.*/
  autodiscover:
    mode: enabled
    ignore_paths:
    - project3
`,
			repoCfg: `
version: 3
automerge: true
projects:
- dir: project1
  workspace: myworkspace
`,
			modifiedFiles: []string{"project1/main.tf", "project2/main.tf", "project3/main.tf"},
			expLen:        2,
		},
		"autodiscover skips ignore_paths in repo when configured in global": {
			globalCfg: `
repos:
- id: /.*/
  autodiscover:
    mode: enabled
    ignore_paths:
    - project[0-9]
`,
			repoCfg: `
version: 3
automerge: true
projects:
- dir: project1
  workspace: myworkspace
autodiscover:
  mode: enabled
  ignore_paths:
  - project3
`,
			modifiedFiles: []string{"project1/main.tf", "project2/main.tf", "project3/main.tf"},
			expLen:        1,
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tmp := DirStructure(t, map[string]any{
				"project1": map[string]any{
					"main.tf": nil,
				},
				"project2": map[string]any{
					"main.tf": nil,
				},
				"project3": map[string]any{
					"main.tf": nil,
				},
			})

			workingDir := NewMockWorkingDir()
			When(workingDir.Clone(Any[logging.SimpleLogging](), Any[models.Repo](), Any[models.PullRequest](),
				Any[string]())).ThenReturn(tmp, nil)
			vcsClient := vcsmocks.NewMockClient()
			When(vcsClient.GetModifiedFiles(Any[logging.SimpleLogging](), Any[models.Repo](),
				Any[models.PullRequest]())).ThenReturn(c.modifiedFiles, nil)

			// Write and parse the global config file.
			globalCfgPath := filepath.Join(tmp, "global.yaml")
			Ok(t, os.WriteFile(globalCfgPath, []byte(c.globalCfg), 0600))
			parser := &config.ParserValidator{}
			globalCfgArgs := valid.GlobalCfgArgs{
				AllowAllRepoSettings: false,
			}

			globalCfg, err := parser.ParseGlobalCfg(globalCfgPath, valid.NewGlobalCfgFromArgs(globalCfgArgs))
			Ok(t, err)

			if c.repoCfg != "" {
				Ok(t, os.WriteFile(filepath.Join(tmp, "atlantis.yaml"), []byte(c.repoCfg), 0600))
			}
			statsScope := metricstest.NewLoggingScope(t, logging.NewNoopLogger(t), "atlantis")

			terraformClient := tfclientmocks.NewMockClient()

			builder := NewProjectCommandBuilder(
				false,
				parser,
				&DefaultProjectFinder{},
				vcsClient,
				workingDir,
				NewDefaultWorkingDirLocker(),
				globalCfg,
				&DefaultPendingPlanFinder{},
				&CommentParser{ExecutableName: "atlantis"},
				false,
				false,
				false,
				false,
				false,
				"",
				"**/*.tf,**/*.tfvars,**/*.tfvars.json,**/terragrunt.hcl,**/.terraform.lock.hcl",
				false,
				"",
				true,
				false,
				"auto",
				statsScope,
				terraformClient,
				&runtime.LocalPlanStore{},
			)

			ctxs, err := builder.BuildPlanCommands(
				&command.Context{
					Log:   logger,
					Scope: statsScope,
				},
				&CommentCommand{
					RepoRelDir: "",
					Flags:      nil,
					Name:       command.Plan,
					Verbose:    false,
				},
			)
			Equals(t, c.expLen, len(ctxs))
			Ok(t, err)

		})
	}
}

func TestDefaultProjectCommandBuilder_AutoDiscoverModeEnabledDefaultsEmptyToAuto(t *testing.T) {
	builder := &DefaultProjectCommandBuilder{
		GlobalCfg: valid.NewGlobalCfgFromArgs(valid.GlobalCfgArgs{}),
	}
	ctx := &command.Context{
		Pull: models.PullRequest{
			BaseRepo: models.Repo{
				FullName: "owner/repo",
				VCSHost: models.VCSHost{
					Hostname: "github.com",
				},
			},
		},
	}

	Equals(t, true, builder.autoDiscoverModeEnabled(ctx, valid.RepoCfg{}))
	Equals(t, false, builder.autoDiscoverModeEnabled(ctx, valid.RepoCfg{
		Projects: []valid.Project{{Dir: "project1"}},
	}))
}

func mustVersion(v string) *version.Version {
	vers, err := version.NewVersion(v)
	if err != nil {
		panic(err)
	}
	return vers
}

var errBuilderCloneReached = errors.New("clone reached")

type builderLockWorkingDir struct {
	WorkingDir
	directory string
	pullDir   string
	clones    int
}

func (d *builderLockWorkingDir) Clone(logging.SimpleLogging, models.Repo, models.PullRequest, string) (string, error) {
	d.clones++
	return "", errBuilderCloneReached
}

func (d *builderLockWorkingDir) GetWorkingDir(models.Repo, models.PullRequest, string) (string, error) {
	return d.directory, nil
}

func (d *builderLockWorkingDir) GetPullDir(models.Repo, models.PullRequest) (string, error) {
	return d.pullDir, nil
}

type builderLockVCS struct{ vcs.Client }

func (builderLockVCS) GetModifiedFiles(logging.SimpleLogging, models.Repo, models.PullRequest) ([]string, error) {
	return []string{"main.tf"}, nil
}

func TestBuilderContendsWithRuntimeWorkingDirectoryLock(t *testing.T) {
	cases := []struct {
		name, project string
		build         func(*DefaultProjectCommandBuilder, *command.Context) ([]command.ProjectContext, error)
	}{
		{"all configured projects", "", func(b *DefaultProjectCommandBuilder, c *command.Context) ([]command.ProjectContext, error) {
			return b.buildAllProjectsByCfg(c, command.Plan, "", nil, false)
		}},
		{"modified projects", "", func(b *DefaultProjectCommandBuilder, c *command.Context) ([]command.ProjectContext, error) {
			return b.buildAllCommandsByCfg(c, command.Plan, "", nil, false)
		}},
		{"named plan", "project", func(b *DefaultProjectCommandBuilder, c *command.Context) ([]command.ProjectContext, error) {
			return b.buildProjectPlanCommand(c, &CommentCommand{Name: command.Plan, ProjectName: "project"})
		}},
		{"named apply", "project", func(b *DefaultProjectCommandBuilder, c *command.Context) ([]command.ProjectContext, error) {
			return b.buildProjectCommand(c, &CommentCommand{Name: command.Apply, ProjectName: "project"})
		}},
	}
	for _, durable := range []bool{false, true} {
		mode := map[bool]string{false: "legacy", true: "durable"}[durable]
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				repo := models.Repo{FullName: "org/repo", VCSHost: models.VCSHost{Hostname: "git.example"}}
				if durable {
					repo.Execution = &models.Execution{Owner: "running-command"}
				}
				pull := models.PullRequest{Num: 1, BaseRepo: repo, Execution: repo.Execution}
				locker := NewDefaultWorkingDirLocker()
				release, err := locker.TryLock(WorkingDirectoryRepoKey(repo), pull.Num, DefaultWorkspace, DefaultRepoRelDir, tc.project, command.Plan, WorkingDirLockMetadata{})
				Ok(t, err)
				defer release()
				directory := &builderLockWorkingDir{directory: t.TempDir()}
				builder := &DefaultProjectCommandBuilder{WorkingDirLocker: locker, WorkingDir: directory, VCSClient: builderLockVCS{}, IncludeGitUntrackedFiles: true, ParserValidator: &config.ParserValidator{}, GlobalCfg: valid.NewGlobalCfgFromArgs(valid.GlobalCfgArgs{})}
				_, err = tc.build(builder, &command.Context{Pull: pull, HeadRepo: repo, Log: logging.NewNoopLogger(t)})
				ErrContains(t, "currently locked", err)
				Equals(t, 0, directory.clones)
			})
		}
	}
}

func TestBuilderWorkingDirectoryLockSeparatesVCSInstances(t *testing.T) {
	repo := models.Repo{FullName: "org/repo", VCSHost: models.VCSHost{Hostname: "first.example"}, Execution: &models.Execution{Owner: "running-command"}}
	locker := NewDefaultWorkingDirLocker()
	release, err := locker.TryLock(WorkingDirectoryRepoKey(repo), 1, DefaultWorkspace, DefaultRepoRelDir, "", command.Plan, WorkingDirLockMetadata{})
	Ok(t, err)
	defer release()
	repo.VCSHost.Hostname = "second.example"
	directory := &builderLockWorkingDir{directory: t.TempDir()}
	builder := &DefaultProjectCommandBuilder{WorkingDirLocker: locker, WorkingDir: directory}
	_, err = builder.buildAllProjectsByCfg(&command.Context{Pull: models.PullRequest{Num: 1, BaseRepo: repo, Execution: repo.Execution}, HeadRepo: repo, Log: logging.NewNoopLogger(t)}, command.Plan, "", nil, false)
	Assert(t, errors.Is(err, errBuilderCloneReached), "another VCS instance was blocked: %v", err)
	Equals(t, 1, directory.clones)
}

func TestApplyAllBuilderUsesRuntimeLockKeys(t *testing.T) {
	for _, activePlan := range []bool{false, true} {
		name := map[bool]string{false: "project workspace", true: "active plan"}[activePlan]
		t.Run(name, func(t *testing.T) {
			repo := models.Repo{FullName: "org/repo", VCSHost: models.VCSHost{Hostname: "git.example"}, Execution: &models.Execution{Owner: "running-command"}}
			pull := models.PullRequest{Num: 1, BaseRepo: repo, Execution: repo.Execution, HeadCommit: "revision", BaseBranch: "main"}
			root := t.TempDir()
			directory := filepath.Join(root, DefaultWorkspace)
			Ok(t, os.MkdirAll(directory, 0700))
			output, err := exec.Command("git", "init", directory).CombinedOutput()
			Assert(t, err == nil, "git init: %v: %s", err, output)
			Ok(t, os.WriteFile(filepath.Join(directory, "default.tfplan"), []byte("saved plan"), 0600))
			locker := NewDefaultWorkingDirLocker()
			heldCommand := command.Version
			if activePlan {
				heldCommand = command.Plan
			}
			release, err := locker.TryLock(WorkingDirectoryRepoKey(repo), pull.Num, DefaultWorkspace, DefaultRepoRelDir, "", heldCommand, WorkingDirLockMetadata{})
			Ok(t, err)
			defer release()
			workingDir := &builderLockWorkingDir{directory: directory, pullDir: root}
			builder := &DefaultProjectCommandBuilder{WorkingDirLocker: locker, WorkingDir: workingDir, PendingPlanFinder: &DefaultPendingPlanFinder{}, ParserValidator: &config.ParserValidator{}, GlobalCfg: valid.NewGlobalCfgFromArgs(valid.GlobalCfgArgs{})}
			ctx := &command.Context{Pull: pull, HeadRepo: repo, Log: logging.NewNoopLogger(t), PullStatus: &models.PullStatus{Pull: pull, Projects: []models.ProjectStatus{{RepoRelDir: DefaultRepoRelDir, Workspace: DefaultWorkspace, Status: models.PlannedPlanStatus}}}}
			_, err = builder.buildAllProjectCommandsByPlan(ctx, &CommentCommand{Name: command.Apply})
			if activePlan {
				ErrContains(t, "a plan is currently running", err)
			} else {
				ErrContains(t, "currently locked", err)
			}
		})
	}
}
