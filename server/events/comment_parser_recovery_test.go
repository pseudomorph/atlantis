// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events_test

import (
	"strings"
	"testing"

	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestUnlockRecoveryParsingAndHelpGate(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "durable"}[enabled], func(t *testing.T) {
			parser := events.CommentParser{ExecutableName: "atlantis", AllowCommands: []command.Name{command.Unlock}, UnlockRecoveryEnabled: enabled}
			plain := parser.Parse("atlantis unlock", models.Github)
			Assert(t, plain.Command != nil, "plain unlock was rejected")
			Assert(t, !plain.Command.ConfirmStopped, "plain unlock attested stopped work")
			result := parser.Parse("atlantis unlock --confirm-stopped", models.Github)
			if enabled {
				Assert(t, result.Command != nil, "durable recovery flag was rejected")
				Assert(t, result.Command.ConfirmStopped, "attestation was not parsed")
			} else {
				Assert(t, result.Command == nil, "legacy unlock accepted a recovery-only flag")
				Equals(t, UnlockUsage, result.CommentResponse)
			}
			for _, comment := range []string{"atlantis unlock --help", "atlantis help", "atlantis unlock -d ."} {
				help := parser.Parse(comment, models.Github)
				Equals(t, enabled, strings.Contains(help.CommentResponse, "--confirm-stopped"))
			}
		})
	}
}
