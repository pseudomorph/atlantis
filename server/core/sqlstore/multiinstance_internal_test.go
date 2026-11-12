// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	. "github.com/runatlantis/atlantis/testing"
)

// Servers sharing a PostgreSQL schema each hold their own claims. Starting a
// second server leaves the first server's live claims alone, and its sweep
// marks them uncertain only after their heartbeat expires. Expiry never hands
// the claim over: recovery still needs the stopped-work attestation.
func TestServersSharePostgresSchema(t *testing.T) {
	d := PostgresDatabase(t)
	first := d.Store(t)
	p := claimed(t, first, claimPull())
	j := claimJob(p, uuid.NewString())
	Ok(t, first.StartJob(j))

	second := d.OtherServer(t).Store(t)
	Assert(t, first.InstanceID() != second.InstanceID(), "servers share an instance ID")
	Ok(t, second.MarkPreviousExecutionsUncertain())
	expired, err := second.ExpireClaims()
	Ok(t, err)
	Equals(t, 0, expired)
	Equals(t, "active", storedClaim(t, second, p.ExecutionOwner()).State)
	Ok(t, first.CheckClaim(p))
	ErrContains(t, "already owned", second.AcquireClaim(p, "contender"))
	ErrContains(t, "execution is active", second.AcquireUnlockClaim(p, "recovery", "operator", true))

	// The first server stops refreshing its heartbeat.
	old := databaseTime(time.Now().Add(-first.claimExpiry - time.Minute))
	Ok(t, second.db.Model(&executionClaim{}).Where("owner = ?", p.ExecutionOwner()).Update("heartbeat_at", old).Error)
	expired, err = second.ExpireClaims()
	Ok(t, err)
	Equals(t, 1, expired)
	Assert(t, first.CheckClaim(p) != nil, "expired claim still authorized its owner")
	refreshed, err := first.RefreshClaimHeartbeats()
	Ok(t, err)
	Equals(t, int64(0), refreshed)
	Equals(t, "uncertain", storedClaim(t, second, p.ExecutionOwner()).State)
	Assert(t, first.FinishJob(j.ID, "succeeded", "") != nil, "owner of an expired claim published completion")
	ErrContains(t, "already owned", second.AcquireClaim(p, "contender"))

	ErrContains(t, "--confirm-stopped", second.AcquireUnlockClaim(p, "recovery", "operator", false))
	Ok(t, second.AcquireUnlockClaim(p, "recovery", "operator", true))
	var audit struct{ PreviousServerInstanceID, RecoveryServerInstanceID string }
	Ok(t, second.db.Table("execution_recoveries").Take(&audit).Error)
	Equals(t, first.InstanceID(), audit.PreviousServerInstanceID)
	Equals(t, second.InstanceID(), audit.RecoveryServerInstanceID)
	recovered, err := second.Job(j.ID)
	Ok(t, err)
	Equals(t, "interrupted", recovered.State)
}

// Servers starting together against an empty schema apply each migration once.
func TestConcurrentStartupMigratesOnce(t *testing.T) {
	d := PostgresDatabase(t)
	const servers = 4
	stores := make([]*Store, servers)
	errs := make([]error, servers)
	var wg sync.WaitGroup
	for i := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stores[i], errs[i] = d.OtherServer(t).Open()
		}()
	}
	wg.Wait()
	for i := range servers {
		Ok(t, errs[i])
		t.Cleanup(func() { _ = stores[i].Close() })
	}
	var applied []int64
	Ok(t, stores[0].db.Raw("SELECT version_id FROM goose_db_version WHERE is_applied ORDER BY version_id").Scan(&applied).Error)
	files, err := migrationFiles.ReadDir("migrations")
	Ok(t, err)
	want := []int64{0}
	for i := range files {
		want = append(want, int64(i+1))
	}
	Equals(t, want, applied)
}

// One server runs a maintenance pass at a time; other passes are independent.
func TestMaintenancePassRunsOnOneServer(t *testing.T) {
	d := PostgresDatabase(t)
	first, second := d.Store(t), d.OtherServer(t).Store(t)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan bool)
	go func() {
		ran, err := first.RunMaintenance("cleanup", func() { close(entered); <-release })
		if err != nil {
			t.Error(err)
		}
		done <- ran
	}()
	<-entered
	ran, err := second.RunMaintenance("cleanup", func() { t.Error("pass ran on two servers") })
	Ok(t, err)
	Assert(t, !ran, "second server ran a held pass")
	ran, err = second.RunMaintenance("retention", func() {})
	Ok(t, err)
	Assert(t, ran, "an unrelated pass did not run")
	close(release)
	Assert(t, <-done, "first server did not run its pass")
	ran, err = second.RunMaintenance("cleanup", func() {})
	Ok(t, err)
	Assert(t, ran, "pass lock was not released")

	// Another schema in the same database has its own passes.
	other := PostgresDatabase(t).Store(t)
	entered, release = make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = first.RunMaintenance("cleanup", func() { close(entered); <-release })
		done <- true
	}()
	<-entered
	ran, err = other.RunMaintenance("cleanup", func() {})
	Ok(t, err)
	Assert(t, ran, "passes in different schemas share a lock")
	close(release)
	<-done
}

// SQLite has one server, so every pass runs.
func TestSQLiteMaintenanceAlwaysRuns(t *testing.T) {
	s := SQLiteDatabase(t).Store(t)
	ran := false
	held, err := s.RunMaintenance("cleanup", func() { ran = true })
	Ok(t, err)
	Assert(t, held && ran, "SQLite skipped a maintenance pass")
}
