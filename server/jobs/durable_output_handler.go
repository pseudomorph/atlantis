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
	journal, err := h.Service.LogService()
	if err != nil {
		return
	}
	var offset int64
	var partial string
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, err := h.Service.Store.Job(id)
		if err != nil || job == nil || job.Private {
			return
		}
		data, next, err := journal.ReadAfter(job.ID, offset)
		if err != nil {
			return
		}
		lines := strings.Split(partial+string(data), "\n")
		partial = lines[len(lines)-1]
		lines = lines[:len(lines)-1]
		for _, line := range lines {
			select {
			case receiver <- line:
			case <-ctx.Done():
				return
			}
		}
		offset = next
		if len(data) != 0 {
			continue
		}
		stream, err := journal.Info(id)
		// A final flush may commit between the empty read and the seal check.
		if err == nil && stream != nil && stream.DeletedAt == nil && stream.ByteLength > offset {
			continue
		}
		if err != nil || stream == nil || stream.SealedAt != nil || job.State != "running" {
			if partial != "" {
				select {
				case receiver <- partial:
				case <-ctx.Done():
				}
			}
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
