// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"testing"

	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestFixedPullAndClaimInvariants(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	p.Execution = &models.Execution{Owner: "owner"}
	Ok(t, s.AcquireClaim(p, "owner"))
	for _, statement := range []string{
		"UPDATE pulls SET closed=TRUE",
		"UPDATE pulls SET cleanup_complete=TRUE",
		"UPDATE pulls SET reopen_requested=TRUE",
		"UPDATE pulls SET closure_generation=-1",
		"UPDATE execution_claims SET owner=''",
		"INSERT INTO pending_pull_lifecycles(pull_id,generation) SELECT id,0 FROM pulls",
	} {
		ErrContains(t, "CHECK constraint failed", s.db.Exec(statement).Error)
	}
	Ok(t, s.CheckClaim(p))
	Ok(t, s.ClosePull(p))
	Ok(t, s.ReleaseClaim(p))
	Ok(t, s.MarkClosedClean(p))
	Ok(t, s.RequestReopen(p))
	Ok(t, s.AcquireClaim(p, "reopened"))
}
