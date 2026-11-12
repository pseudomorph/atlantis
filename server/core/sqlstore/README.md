# Relational storage

The SQLite implementation runs embedded Goose SQL migrations. GORM does not
create or alter the schema. Migrations can transform data with statements such
as `INSERT INTO ... SELECT ...`; Goose runs each transactionally unless it opts out.

Keep schema definitions portable for a future PostgreSQL backend: UUIDs use
`TEXT`, counters use `BIGINT`, binary payloads use `BYTEA`, and booleans use
`TRUE`/`FALSE`. SQLite accepts these declarations without `STRICT` tables.
Store timestamps in UTC, normalized to microseconds. Storage-generated timestamps
use `databaseNow`; explicitly supplied event timestamps retain their instant.
SQLite's clock has millisecond resolution.

`OpenServer` holds a process lock before opening or migrating SQLite. Run one
server on local or block-backed storage; network filesystems are unsupported.
The store holds project locks, global command locks, pull request status, jobs
and execution claims.

Commands own persisted execution claims until completion, and lock, status and
job writes require the live claim. Startup marks leftover claims uncertain,
preventing new commands and destructive cleanup for those PRs or API
invocations: a dead parent does not prove Terraform children stopped. There are
no heartbeats, expiring leases, or automatic retries. Authorized unlock recovery
requires an explicit stopped-work attestation; it records the actor and
interrupts unfinished jobs before releasing reservations. Closing a pull request
records a closure generation; deferred cleanup holds its own claim and must
match that generation, and a reopened pull request accepts work only after the
cleanup completes. Unverified close events persist in `pending_pull_lifecycles`
and block admission until maintenance verifies the live state.

Run storage tests with `go test -race ./server/core/sqlstore`. PostgreSQL and
distributed leases are separate follow-up work.

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
