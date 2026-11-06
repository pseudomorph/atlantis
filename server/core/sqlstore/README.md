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
scopes: a dead parent does not prove Terraform children stopped. There are no
heartbeats, expiring leases, automatic retries, or operator reconciliation yet.

Run storage tests with `go test -race ./server/core/sqlstore`. PostgreSQL,
distributed leases, and recovery tooling are separate follow-up work.

The unreleased schema uses one initial migration; later changes add new versions.
Recreate databases made with the earlier draft migration sequence.

Native database dumps are not a cross-backend migration format; a future transfer
must copy typed rows into a schema initialized by Goose.
