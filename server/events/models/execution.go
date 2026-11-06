// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package models

import "sync"

// Execution is shared by command contexts and identity adapters; credentials are never serialized.
type Execution struct {
	Owner, Scope string
	Release      func()
	once         sync.Once
}

// Close releases ownership once, including when nested API cleanup paths converge.
func (e *Execution) Close() {
	if e != nil {
		e.once.Do(func() {
			if e.Release != nil {
				e.Release()
			}
		})
	}
}
func (e *Execution) owner() string {
	if e == nil {
		return ""
	}
	return e.Owner
}
func (e *Execution) scope() string {
	if e == nil {
		return ""
	}
	return e.Scope
}
func (p PullRequest) ExecutionOwner() string { return p.Execution.owner() }
func (p PullRequest) ExecutionScope() string { return p.Execution.scope() }
func (r Repo) ExecutionOwner() string        { return r.Execution.owner() }
func (r Repo) ExecutionScope() string        { return r.Execution.scope() }
