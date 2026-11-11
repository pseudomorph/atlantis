// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package jobs

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
)

// DurableOutputHandler reads history from storage; subscriptions hold no authoritative output.
type DurableOutputHandler struct {
	Service   *Service
	mu        sync.Mutex
	receivers map[chan string]context.CancelFunc
}

func (h *DurableOutputHandler) Send(ctx command.ProjectContext, msg string, complete bool) {
	if complete || ctx.SuppressJobOutput || ctx.JobID == "" {
		return
	}
	if err := h.Service.AppendLog(ctx.JobID, msg); err != nil {
		ctx.Log.Err("persisting job output %q %v", ctx.JobID, err)
	}
}
func (h *DurableOutputHandler) SendWorkflowHook(ctx models.WorkflowHookCommandContext, msg string, complete bool) {
	if ctx.SuppressJobOutput || complete {
		return
	}
	if err := h.Service.AppendLog(ctx.HookID, msg); err != nil {
		ctx.Log.Err("persisting workflow hook %q %v", ctx.HookID, err)
	}
}

func (h *DurableOutputHandler) Handle()            {}
func (h *DurableOutputHandler) CleanUp(_ PullInfo) {}
func (h *DurableOutputHandler) IsKeyExists(id string) bool {
	job, err := h.Service.Store.Job(id)
	return err == nil && job != nil && !job.Private
}
func (h *DurableOutputHandler) Register(id string, receiver chan string) {
	ctx, cancel := context.WithCancel(context.Background())
	h.mu.Lock()
	if h.receivers == nil {
		h.receivers = map[chan string]context.CancelFunc{}
	}
	h.receivers[receiver] = cancel
	h.mu.Unlock()
	defer func() { cancel(); h.mu.Lock(); delete(h.receivers, receiver); h.mu.Unlock(); close(receiver) }()
	var sequence int64
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, err := h.Service.Store.Job(id)
		if err != nil || job == nil || job.Private {
			return
		}
		data, next, err := h.Service.Artifacts.ReadLogAfter(*job, sequence)
		if err != nil {
			return
		}
		lines := strings.Split(string(data), "\n")
		lines = lines[:len(lines)-1]
		for _, line := range lines {
			select {
			case receiver <- line:
			case <-ctx.Done():
				return
			}
		}
		sequence = next
		if job.State != "running" {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (h *DurableOutputHandler) Deregister(_ string, receiver chan string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cancel := h.receivers[receiver]; cancel != nil {
		cancel()
	}
}
func (h *DurableOutputHandler) GetPullToJobMapping() []PullInfoWithJobIDs {
	rows, err := h.Service.Store.Jobs()
	if err != nil {
		return nil
	}
	groups := map[PullInfo][]JobIDInfo{}
	for _, j := range rows {
		p := PullInfo{PullNum: j.Pull.Num, Repo: j.Pull.BaseRepo.Name, RepoFullName: j.Pull.BaseRepo.FullName, ProjectName: j.Project.ProjectName, Path: j.Project.Path, Workspace: j.Workspace}
		groups[p] = append(groups[p], JobIDInfo{JobID: j.ID, Time: j.StartedAt, JobStep: j.Command})
	}
	var result []PullInfoWithJobIDs
	for p, ids := range groups {
		result = append(result, PullInfoWithJobIDs{Pull: p, JobIDInfos: ids})
	}
	return result
}
