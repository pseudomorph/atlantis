// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package models_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestExecutionAuthorityIsNotSerialized(t *testing.T) {
	execution := &models.Execution{Owner: "private-owner", Scope: "private-scope", Release: func() {}}
	repo := models.Repo{FullName: "org/repo", Execution: execution}
	pull := models.PullRequest{BaseRepo: repo, Execution: execution}
	data, err := json.Marshal(pull)
	Ok(t, err)
	Assert(t, !strings.Contains(string(data), "private-"), "execution authority leaked into stored or public identity")
}
