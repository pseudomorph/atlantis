# Relational storage

The store runs on SQLite (`OpenServer`) or PostgreSQL (`OpenPostgresServer`).
Both run the same embedded Goose SQL migrations, through Goose's `sqlite3` and
`postgres` dialects. GORM does not create or alter the schema. Migrations can
transform data with statements such as `INSERT INTO ... SELECT ...`; Goose runs
each transactionally unless it opts out.

Schema definitions are portable between the two: UUIDs use `TEXT`, counters use
`BIGINT`, binary payloads use `BYTEA`, booleans use `TRUE`/`FALSE`, and
timestamps use `TIMESTAMP` without a time zone. SQLite accepts these
declarations without `STRICT` tables. Store timestamps in UTC, normalized to
microseconds with `databaseTime`; pgx writes a `TIMESTAMP` from a value's wall
clock, so a non-UTC value would shift its instant. Storage-generated timestamps
and production retention cutoffs use `databaseNow`, which reads SQLite's
`strftime('now')` or PostgreSQL's `clock_timestamp()`; explicitly supplied event
timestamps retain their instant. SQLite's clock has millisecond resolution.

Both open functions lock the data directory with a file lock, so each server
needs its own `data-dir`. SQLite runs one server on local or block-backed
storage; network filesystems are unsupported. Several servers may share one
PostgreSQL schema. `OpenPostgresServer` runs Goose migrations under Goose's
PostgreSQL session locker, an advisory lock keyed by the schema in
`search_path`, so servers that start together migrate once while the others
wait (`TestConcurrentStartupMigratesOnce`). A server refuses to start against a
schema newer than it supports.

`RunMaintenance` runs each maintenance pass (lifecycle verification, deferred
cleanup, and log archiving with retention) under a session-level advisory lock
taken with `pg_try_advisory_lock` on a dedicated connection, so one server runs
a given pass at a time and the others skip it. The lock only avoids duplicate
work and duplicate VCS comments; correctness still comes from the row-lock
rule below, because a lost connection ends the lock while its pass may still
be running. For the same reason a log archive write that lands after
retention expired the log is deleted by the writer when its handoff finds
`deleted_at` set. SQLite runs every pass.

The store holds project locks, global command locks, pull request status, jobs,
execution claims, pending plans, plan snapshot metadata, job log journals and
recent webhook delivery IDs.

Commands own persisted execution claims until completion, and lock, status and
job writes require the live claim. Startup marks leftover claims uncertain,
preventing new commands and destructive cleanup for those PRs or API
invocations: a dead parent does not prove Terraform children stopped. There are
no automatic retries. Authorized unlock recovery requires an explicit
stopped-work attestation; it records the actor and the previous and recovering
server instances, interrupts unfinished jobs and invalidates pending plans before
releasing reservations. Closing a pull request
records a closure generation; deferred cleanup holds its own claim and must
match that generation, and a reopened pull request accepts work only after the
cleanup completes. Unverified close events persist in `pending_pull_lifecycles`
and block admission until maintenance verifies the live state.

Each `Store` gets a new server instance ID when it opens. Claims record that
instance and a heartbeat; jobs record the instance that ran them.
`RefreshClaimHeartbeats` updates the instance's active claims every
`ClaimHeartbeatInterval` in one conditional `UPDATE`, so it never revives a claim
that a sweep or recovery changed. `ExpireClaims` locks each candidate claim,
rechecks its heartbeat against `ClaimExpiry` in Go, marks it uncertain and seals
its owner's open logs as interrupted. Recovery treats an active claim with an
expired heartbeat as uncertain. Expiry never transfers a claim. Heartbeat
comparisons use database time read by `databaseNow`. With SQLite the data
directory lock proves earlier servers exited, so startup marks claims from
every earlier instance uncertain without waiting for expiry. With PostgreSQL,
`MarkPreviousExecutionsUncertain` changes nothing: another server may hold live
claims, and this process's instance ID is new, so it has no earlier claims.
Claims of a server that stopped become uncertain when any server's sweep finds
their heartbeat expired (`TestServersSharePostgresSchema`).

`RecordDelivery` inserts a webhook's provider and delivery ID into
`webhook_deliveries` with `ON CONFLICT DO NOTHING`; the affected row count
tells servers that receive the same delivery which one handles it.
`ForgetDelivery` removes a delivery whose response was not a success, so the
provider's retry is handled. Retention deletes rows older than
`DeliveryRetention`.

Run storage tests with `go test -race ./server/core/sqlstore`. They run on
SQLite, and on PostgreSQL too when `ATLANTIS_TEST_POSTGRES_DSN` holds a
connection string; each PostgreSQL test creates and drops its own schema.
`ForEachDatabase` runs a test on both. Locally:

```bash
docker run -d --name atlantis-pg -e POSTGRES_PASSWORD=atlantis -e POSTGRES_DB=atlantis -p 55432:5432 postgres:17
ATLANTIS_TEST_POSTGRES_DSN='postgres://postgres:atlantis@localhost:55432/atlantis?sslmode=disable' go test -race ./server/core/sqlstore
```

Set `ATLANTIS_TEST_DURABLE_DATABASE=postgres` as well to run the durable
end-to-end tests in `server/controllers/events` on PostgreSQL. The
multi-server acceptance tests there (`durable_multiinstance_e2e_test.go` and
`durable_log_viewer_e2e_test.go`) start two servers on one schema whenever
`ATLANTIS_TEST_POSTGRES_DSN` is set.

Each schema change adds a new numbered file under `migrations/`, and released
migrations are never edited. An existing database upgrades by applying the newer
files in order. SQLite cannot add a table constraint in place, so a change to an
existing table adds columns with defaults, puts new checks on the added columns,
or adds a table keyed by the parent row.

SQLite serializes writers with `_txlock=immediate` and one connection, but
correctness does not depend on that. PostgreSQL runs transactions concurrently
under `READ COMMITTED`. A transaction that reads pull-scoped state
and writes based on it first locks the governing `pulls` row with `lockPull`
(then the execution claim, when it has one). Every claim, job, lock, status and
lifecycle row belongs to a pull, so the lock also fences inserts that a
missing-row check cannot. Other writes are single conditional `UPDATE` or
`DELETE` statements whose affected row count is checked. The SQLite dialector
omits `FOR UPDATE`; the PostgreSQL dialector emits `FOR UPDATE` and `FOR SHARE`
(`TestRowLockEmittedByPostgres`), and `concurrency_internal_test.go` races
release against admission and checks that a lock waiter sees the committed state.

Log reads follow the same rule. `ReadLogJournal` locks the `job_logs` row
before it reads `job_log_chunks`, and appends, archive handoff and retention lock
or update that row before they add or delete chunks. Under `READ COMMITTED` each
statement sees its own snapshot, so without the row lock a reader could see a
journal marker and then find its chunks deleted by a concurrent archive handoff.
With it, a reader sees either the complete journal or the archive marker.

Native database dumps are not a cross-backend migration format; a future transfer
must copy typed rows into a schema initialized by Goose.

The experiment requires a 64-bit server. Repositories are identified by VCS
instance (host, port and installation path) plus full name, so two instances
with the same repository name never share locks or status. Lock keys encode that
identity and never contain credentials. Synthetic non-PR pull numbers are
insert-only identities reserved before checkout; collisions never upsert an old
invocation. There is no execution-scope table: jobs, claims and recovery audits
reference `pulls.id`. Negative rows represent existing synchronous API
invocations; only the matching claim owner can revisit one. Credential-bearing
clone URLs are never persisted.

Store one credential-free clone URL and populate both repository model URL fields
when loading it. Repository owner/name/hostname derive on load. Status rows
reference their pull; project status constraints separately enforce repository
matching and deletion with the parent status.

A plan job records its pending plan in `pending_plans`, one row per project and
pull. `StartPlan` takes the slot when the runner holds its locks, and a plan
becomes ready only if it finishes successfully with a published snapshot while
its pull is open. A later plan for the same project replaces the slot; the older
job then fails at completion. Apply, policy check, import and state commands
bind the ready producer at admission with an insert-only `plan_bindings` row,
whose foreign keys require the producer's project and pull.
Closure, unlock, recovery, pull status deletion and the release of a non-PR
invocation delete pending rows. A successful apply, import or state command
consumes its plan, and so does one that fails after it started mutating.

Each snapshot and database blob is keyed by its producing job; filesystem keys
derive from the same ID under `snapshots/v1/`. The manifest records member
roles, paths and checksums, and publication requires the live claim. Retention
protects the bundle while any consumer is running, even after pending-plan
invalidation, then purges it once `storage.retention.plan_artifacts` has passed.
Orphan collection deletes unreferenced bundles only while no claim or job is active.

Logs use separate byte-offset journals and never participate in plan selection.
Running output always goes to the journal. With filesystem artifacts, a sealed
log is archived once under `logs/v1/<job>` and its journal bytes are deleted in
the transaction that records the archive; with database artifacts, the journal
is the final copy. Workflow hooks have no project; project commands require one.
SQL enforces these cardinalities.
