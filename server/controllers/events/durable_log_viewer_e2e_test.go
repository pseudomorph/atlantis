//go:build unix

// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/runatlantis/atlantis/server/jobs"
	. "github.com/runatlantis/atlantis/testing"
)

// A log viewer on one server follows the output of an apply running on the
// other server until the job finishes.
func TestDurableStorageLogViewerFollowsOtherInstance(t *testing.T) {
	skipUnlessDurableE2E(t)
	skipUnlessTerraformData(t)
	repoDir, headSHA := initializeRepo(t, "simple")
	signals := t.TempDir()
	entered, proceed := filepath.Join(signals, "entered"), filepath.Join(signals, "proceed")
	configuration := fmt.Sprintf(`resource "terraform_data" "log_probe" {
  provisioner "local-exec" {
    command = "echo provisioner started; touch '%s'; while [ ! -e '%s' ]; do sleep 0.1; done; echo provisioner finished"
  }
}
`, entered, proceed)
	Ok(t, os.WriteFile(filepath.Join(repoDir, "probe.tf"), []byte(configuration), 0600))
	runCmd(t, repoDir, "git", "add", "probe.tf")
	runCmd(t, repoDir, "git", "commit", "-m", "slow apply")
	headSHA = strings.TrimSpace(runCmd(t, repoDir, "git", "rev-parse", "HEAD"))
	a, b := startSharedServers(t, repoDir, headSHA)
	a.open(t, headSHA)
	lastReplyContains(t, a, "Plan: 4 to add")

	applied := make(chan struct{})
	go func() {
		defer close(applied)
		a.comment(t, "atlantis apply")
	}()
	t.Cleanup(func() { _ = os.WriteFile(proceed, nil, 0600); <-applied })
	deadline := time.After(time.Minute)
	for {
		if _, err := os.Stat(entered); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("apply did not reach its provisioner")
		case <-time.After(20 * time.Millisecond):
		}
	}
	var jobID string
	all, err := b.storage.Store.Jobs()
	Ok(t, err)
	for _, job := range all {
		if job.Command == "apply" && job.State == "running" {
			jobID = job.ID
		}
	}
	Assert(t, jobID != "", "no running apply job")

	viewer := &jobs.DurableOutputHandler{Service: b.storage.Jobs}
	Assert(t, viewer.IsKeyExists(jobID), "viewer cannot find the job running on the other server")
	lines := make(chan string, 1024)
	go viewer.Register(jobID, lines)
	read := func(want string) {
		t.Helper()
		timeout := time.After(time.Minute)
		for {
			select {
			case line, ok := <-lines:
				Assert(t, ok, "viewer stopped before %q", want)
				if strings.Contains(line, want) {
					return
				}
			case <-timeout:
				t.Fatalf("viewer did not receive %q", want)
			}
		}
	}
	read("provisioner started")
	Ok(t, os.WriteFile(proceed, nil, 0600))
	read("provisioner finished")
	read("Apply complete!")
	select {
	case <-applied:
	case <-time.After(time.Minute):
		t.Fatal("apply did not finish")
	}
	timeout := time.After(time.Minute)
	for {
		select {
		case _, ok := <-lines:
			if !ok {
				lastReplyContains(t, a, "Apply complete!")
				return
			}
		case <-timeout:
			t.Fatal("viewer kept following a finished job")
		}
	}
}
