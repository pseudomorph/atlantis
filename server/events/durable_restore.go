// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"encoding/json"
	"fmt"

	"github.com/runatlantis/atlantis/server/core/config/valid"
	"github.com/runatlantis/atlantis/server/core/runtime"
	"github.com/runatlantis/atlantis/server/events/command"
)

// prepareDurableRestore applies only to managed saved-plan consumers. Custom
// workflows retain control of their initialization and plan-file contract.
func (p *DefaultProjectCommandRunner) prepareDurableRestore(ctx command.ProjectContext) (command.ProjectContext, []string, bool, error) {
	if p.Jobs == nil || p.Artifacts == nil || !requiresManagedPlanFileForApply(ctx) || (ctx.CommandName != command.Apply && ctx.CommandName != command.PolicyCheck) {
		return ctx, nil, false, nil
	}
	job, err := p.Jobs.Store.Job(ctx.JobID)
	if err != nil {
		return ctx, nil, false, err
	}
	if job == nil || job.PlanJobID == "" {
		return ctx, nil, false, fmt.Errorf("no selected durable plan; run plan again")
	}
	plan, err := p.Jobs.Store.Job(job.PlanJobID)
	if err != nil {
		return ctx, nil, false, err
	}
	if plan == nil || ctx.TerraformVersion == nil || ctx.TerraformDistribution == nil || plan.ToolVersion == "" || plan.ToolDistribution == "" {
		return ctx, nil, false, fmt.Errorf("pending plan lacks resolved Terraform distribution/version; run plan again")
	}
	if plan.ToolVersion != ctx.TerraformVersion.String() || plan.ToolDistribution != *ctx.TerraformDistribution {
		return ctx, nil, false, fmt.Errorf("pending plan used %s %s but this command selects %s %s; run plan again", plan.ToolDistribution, plan.ToolVersion, *ctx.TerraformDistribution, ctx.TerraformVersion)
	}
	inputs, err := p.Artifacts.Records.InputArtifacts(ctx.JobID)
	if err != nil {
		return ctx, nil, false, err
	}
	haveLock := false
	for _, artifact := range inputs {
		if artifact.Kind == "lockfile" {
			haveLock = true
			ctx.RestoredLockHash = artifact.SHA256
		}
	}
	// Old Terraform ignores lock files even when a repository happens to contain one.
	if !haveLock || (plan.ToolDistribution == "terraform" && runtime.MustConstraint("< 0.14.0").Check(ctx.TerraformVersion.Core())) {
		return ctx, nil, false, nil
	}
	manifest, err := p.Artifacts.InputRuntime(ctx.JobID)
	if err != nil {
		return ctx, nil, false, err
	}
	ctx.RestoredDataDirectory = manifest.DataDirectory
	for _, step := range ctx.Steps {
		if step.StepName == "init" {
			return ctx, nil, false, nil
		}
	}
	var planSteps []valid.Step
	if err := json.Unmarshal([]byte(plan.Steps), &planSteps); err != nil {
		return ctx, nil, false, fmt.Errorf("reading plan initialization steps: %w", err)
	}
	for _, step := range planSteps {
		if step.StepName == "init" {
			return ctx, step.ExtraArgs, true, nil
		}
		if step.StepName != "env" && step.StepName != "multienv" {
			break
		}
	}
	return ctx, nil, false, fmt.Errorf("custom plan setup cannot be initialized automatically; configure an init step in the apply/policy workflow")
}
