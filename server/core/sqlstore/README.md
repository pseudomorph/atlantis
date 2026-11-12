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
Commands own persisted execution claims until completion. Startup marks leftover
claims uncertain, preventing new commands and destructive cleanup for those
PRs or API invocations: a dead parent does not prove Terraform children stopped. There are no
heartbeats, expiring leases, or automatic retries. Authorized PR unlock recovery
requires an explicit stopped-work attestation; it records the actor and interrupts
unfinished jobs before invalidating plans and releasing reservations.

Run storage tests with `go test -race ./server/core/sqlstore`. PostgreSQL,
distributed leases, and recovery tooling are separate follow-up work.

The unreleased schema uses one initial migration; later changes add new versions.
Recreate databases made with the earlier draft migration sequence.

Native database dumps are not a cross-backend migration format; a future transfer
must copy typed rows into a schema initialized by Goose.

The experiment requires a 64-bit server. Synthetic non-PR pull numbers are
insert-only identities reserved before checkout; collisions never upsert an old
invocation. There is no execution-scope table: jobs, claims and recovery audits reference
`pulls.id`. Negative rows represent existing synchronous API invocations; only
the matching claim owner can revisit one. API execution-group design is deferred.
Credential-bearing clone URLs are never persisted.

Store one credential-free clone URL and populate both repository model URL fields
when loading it. Status rows reference their pull; project status constraints
separately enforce repository matching and deletion with the parent status.

Consumers retain an immutable `plan_job_id`. Their inputs are the selected
producer's immutable snapshot. Each snapshot and database blob is keyed by its
producing job; filesystem and S3 keys derive from the same ID. The manifest records
member roles, paths and checksums. Retention protects the bundle while any consumer
is running, even after pending-plan invalidation. Logs use separate byte-offset
journals and never participate in plan selection.

Repository owner/name/hostname derive on load. Workflow hooks have no project;
project commands require one. SQL enforces these cardinalities and fixed lifecycle
invariants; application validation owns expandable state and artifact-role lists.
