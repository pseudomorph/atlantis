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
The store holds project locks, global command locks and pull request status.

Run storage tests with `go test -race ./server/core/sqlstore`. PostgreSQL and
distributed leases are separate follow-up work.

Each schema change adds a new numbered file under `migrations/`, and released
migrations are never edited. An existing database upgrades by applying the newer
files in order. SQLite cannot add a table constraint in place, so a change to an
existing table adds columns with defaults, puts new checks on the added columns,
or adds a table keyed by the parent row.

SQLite serializes writers with `_txlock=immediate` and one connection, but
correctness does not depend on that. A transaction that reads pull-scoped state
and writes based on it first locks the governing `pulls` row with `lockPull`.
Every lock and status row belongs to a pull, so the lock also fences inserts
that a missing-row check cannot. Other writes are single conditional `UPDATE` or
`DELETE` statements whose affected row count is checked. The SQLite dialector
omits `FOR UPDATE`; a backend with row locks under `READ COMMITTED` must honor it.

Native database dumps are not a cross-backend migration format; a future transfer
must copy typed rows into a schema initialized by Goose.

The experiment requires a 64-bit server. Repositories are identified by VCS
instance (host, port and installation path) plus full name, so two instances
with the same repository name never share locks or status. Lock keys encode that
identity and never contain credentials. Credential-bearing clone URLs are never
persisted.

Store one credential-free clone URL and populate both repository model URL fields
when loading it. Repository owner/name/hostname derive on load. Status rows
reference their pull; project status constraints separately enforce repository
matching and deletion with the parent status.
