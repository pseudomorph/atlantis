// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package models

import "time"

type JobKind string

const CommandJob JobKind = "command"

type Job struct {
	Kind       JobKind
	ID         string
	Owner      string `json:"-"`
	Pull       PullRequest
	Project    Project
	Workspace  string
	Command    string
	SubCommand string
	Arguments  []string
	Steps      string
	Issuer     User
	Trigger    string
	Revision   string
	State      string
	Error      string
	Private    bool
	StartedAt  time.Time
	FinishedAt *time.Time
}

// JobStep preserves workflow order without persisting commands or argument values.
type JobStep struct {
	Name         string `json:"name"`
	HasExtraArgs bool   `json:"has_extra_args,omitempty"`
}
