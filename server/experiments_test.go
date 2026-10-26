// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"testing"

	"github.com/runatlantis/atlantis/server"
	. "github.com/runatlantis/atlantis/testing"
)

func TestExperimentsPreserveExistingDriftConfiguration(t *testing.T) {
	for _, names := range [][]string{nil, {"drift-detection"}, {"drift-detection", "drift-detection"}} {
		cfg := server.UserConfig{Experiments: names, EnableDriftDetection: true, EnableDriftRemediation: true}
		Ok(t, cfg.ValidateExperiments())
	}
}

func TestNewServerRejectsUnknownExperimentBeforeInitialization(t *testing.T) {
	_, err := server.NewServer(server.UserConfig{Experiments: []string{"unknown"}}, server.Config{})
	ErrContains(t, `unknown server experiment "unknown"`, err)
}
