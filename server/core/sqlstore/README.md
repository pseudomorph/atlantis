# Relational storage

The SQLite implementation runs embedded Goose SQL migrations. GORM does not
create or alter the schema. Migrations can transform data with statements such
as `INSERT INTO ... SELECT ...`; Goose runs each transactionally unless it opts out.

Keep schema definitions portable for a future PostgreSQL backend: UUIDs use
`TEXT`, counters use `BIGINT`, binary payloads use `BYTEA`, and booleans use
`TRUE`/`FALSE`. SQLite accepts these declarations without `STRICT` tables.
Store timestamps in UTC, normalized to microseconds. Storage-generated timestamps
and production retention cutoffs use `databaseNow`; explicitly supplied event
timestamps retain their instant. SQLite's clock has millisecond resolution.

`OpenServer` holds a process lock before opening or migrating SQLite. Run one
server on local or block-backed storage; network filesystems are unsupported.
The store holds project locks, global command locks, pull request status, jobs,
execution claims, pending plans, plan snapshot metadata and job log journals.

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
comparisons use database time read by `databaseNow`. With SQLite, startup marks
claims from every earlier instance uncertain without waiting for expiry.

Run storage tests with `go test -race ./server/core/sqlstore`. PostgreSQL and
multi-server execution are separate follow-up work.

Each schema change adds a new numbered file under `migrations/`, and released
migrations are never edited. An existing database upgrades by applying the newer
files in order. SQLite cannot add a table constraint in place, so a change to an
existing table adds columns with defaults, puts new checks on the added columns,
or adds a table keyed by the parent row.

SQLite serializes writers with `_txlock=immediate` and one connection, but
correctness does not depend on that. A transaction that reads pull-scoped state
and writes based on it first locks the governing `pulls` row with `lockPull`
(then the execution claim, when it has one). Every claim, job, lock, status and
lifecycle row belongs to a pull, so the lock also fences inserts that a
missing-row check cannot. Other writes are single conditional `UPDATE` or
`DELETE` statements whose affected row count is checked. The SQLite dialector
omits `FOR UPDATE`; a backend with row locks under `READ COMMITTED` must honor it.

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
