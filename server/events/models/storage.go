// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package models

import "time"

type JobKind string

const (
	CommandJob      JobKind = "command"
	WorkflowHookJob JobKind = "workflow_hook"
)

type Job struct {
	ToolDistribution string
	ToolVersion      string
	Kind             JobKind
	ManagedPlan      bool
	ID               string
	Owner            string `json:"-"`
	Pull             PullRequest
	Project          Project
	Workspace        string
	Command          string
	SubCommand       string
	Arguments        []string
	Steps            string
	Issuer           User
	Trigger          string
	Revision         string
	PlanJobID        string
	State            string
	Error            string
	Private          bool
	StartedAt        time.Time
	FinishedAt       *time.Time
}

type Artifact struct {
	Sequence     int64
	RelativePath string
	ID           string
	JobID        string
	Kind         string
	Location     string
	Size         int64
	SHA256       string
	CreatedAt    time.Time
}
