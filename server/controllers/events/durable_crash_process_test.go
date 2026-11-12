//go:build unix

// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/go-version"

	. "github.com/runatlantis/atlantis/testing"
)

// The helper is killed while a real Terraform apply is inside a local-exec
// provisioner. Its Terraform child outlives it and completes independently.
func TestDurableStorageCrashProcessHelper(t *testing.T) {
	if os.Getenv("ATLANTIS_DURABLE_CRASH_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	skipUnlessDurableE2E(t)
	s := startDurableServer(t, os.Getenv("ATLANTIS_DURABLE_REPO"), os.Getenv("ATLANTIS_DURABLE_SHA"), &durableE2E{DataDir: os.Getenv("ATLANTIS_DURABLE_DATA")})
	s.open(t, os.Getenv("ATLANTIS_DURABLE_SHA"))
	lastReplyContains(t, s, "Plan: 4 to add")
	s.comment(t, "atlantis apply")
}

func TestDurableStorageCrashDuringApplyAndUnlockRecovery(t *testing.T) {
	skipUnlessDurableE2E(t)
	versionOutput, err := exec.Command("terraform", "version").Output()
	Ok(t, err)
	match := versionRegex.FindStringSubmatch(string(versionOutput))
	if len(match) < 2 {
		t.Fatal("could not identify Terraform version")
	}
	installed := version.Must(version.NewVersion(match[1]))
	if installed.LessThan(version.Must(version.NewVersion("1.4.0"))) {
		t.Skip("terraform_data process test requires Terraform >= 1.4")
	}
	repoDir, headSHA := initializeRepo(t, "simple")
	signals := t.TempDir()
	ready, done := filepath.Join(signals, "entered"), filepath.Join(signals, "finished")
	pidFile := filepath.Join(signals, "terraform.pid")
	// terraform_data requires Terraform >= 1.4; this process test is independent
	// of the older-version restored-init argument tests.
	file := filepath.Join(repoDir, "crash.tf")
	configuration := fmt.Sprintf(`resource "terraform_data" "crash_probe" {
  provisioner "local-exec" {
    command = "echo $PPID > '%s'; touch '%s'; sleep 4; touch '%s'"
  }
}
`, pidFile, ready, done)
	Ok(t, os.WriteFile(file, []byte(configuration), 0600))
	runCmd(t, repoDir, "git", "add", "crash.tf")
	runCmd(t, repoDir, "git", "commit", "-m", "controlled apply interruption")
	headSHA = strings.TrimSpace(runCmd(t, repoDir, "git", "rev-parse", "HEAD"))
	dataDir := t.TempDir()
	executable, err := os.Executable()
	Ok(t, err)
	child := exec.Command(executable, "-test.run=^TestDurableStorageCrashProcessHelper$", "-test.timeout=90s")
	child.Env = append(os.Environ(), "ATLANTIS_DURABLE_CRASH_HELPER=1", "ATLANTIS_DURABLE_REPO="+repoDir, "ATLANTIS_DURABLE_SHA="+headSHA, "ATLANTIS_DURABLE_DATA="+dataDir, "ATLANTIS_DURABLE_DSN="+durableDSN(t, dataDir))
	output, err := os.Create(filepath.Join(signals, "helper.log"))
	Ok(t, err)
	defer output.Close()
	child.Stdout, child.Stderr = output, output
	Ok(t, child.Start())
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	t.Cleanup(func() { _ = child.Process.Kill() })
	deadline := time.After(60 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case err := <-exited:
			data, _ := os.ReadFile(output.Name())
			t.Fatalf("helper exited before apply interruption: %v\n%s", err, data)
		case <-deadline:
			t.Fatal("apply did not reach its provisioner")
		case <-time.After(20 * time.Millisecond):
		}
	}
	Ok(t, child.Process.Kill())
	<-exited

	restarted := startDurableServer(t, repoDir, headSHA, &durableE2E{DataDir: dataDir})
	before, err := restarted.storage.Store.Jobs()
	Ok(t, err)
	restarted.comment(t, "atlantis apply")
	lastReplyContains(t, restarted, "uncertain")
	after, err := restarted.storage.Store.Jobs()
	Ok(t, err)
	Equals(t, len(before), len(after))
	restarted.comment(t, "atlantis unlock")
	lastReplyContains(t, restarted, "--confirm-stopped")

	// The child completes after the Atlantis process has died. Never attest
	// stopped execution until that fact has been established independently.
	deadline = time.After(30 * time.Second)
	for {
		if _, err := os.Stat(done); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("orphaned Terraform child did not complete")
		case <-time.After(20 * time.Millisecond):
		}
	}
	pidBytes, err := os.ReadFile(pidFile)
	Ok(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	Ok(t, err)
	deadline = time.After(10 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Terraform child is still running")
		case <-time.After(20 * time.Millisecond):
		}
	}

	restarted.comment(t, "atlantis unlock --confirm-stopped")
	lastReplyContains(t, restarted, "unlocked and plans discarded")
	jobs, err := restarted.storage.Store.Jobs()
	Ok(t, err)
	interrupted := false
	for _, job := range jobs {
		if job.Command == "apply" && job.State == "interrupted" {
			interrupted = true
		}
	}
	Assert(t, interrupted, "recovery did not preserve interrupted apply history")
	locks, err := restarted.storage.Store.List()
	Ok(t, err)
	Equals(t, 0, len(locks))
}
