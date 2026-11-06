# Relational storage

SQLite and PostgreSQL run the same embedded Goose SQL migrations. GORM does not
create or alter the schema. SQL migrations can also transform data with statements
such as `INSERT INTO ... SELECT ...`; Goose runs each migration transactionally
unless it explicitly opts out.

Keep migrations portable: application-generated UUIDs use `TEXT`, counters use
`BIGINT`, and booleans use `TRUE`/`FALSE`. Store `TIMESTAMP` values in UTC; normalize
caller-supplied and automatic times to UTC microseconds before writing them.
Storage-generated timestamps and production retention cutoffs use the database
clock through `databaseNow`, which converts backend results to the same UTC
representation without changing the schema. Explicitly supplied event timestamps
are preserved and normalized; zero job/artifact timestamps are filled on insertion.
SQLite's clock has millisecond resolution; normalization does not invent finer precision.

Shared migrations do not make native database dumps interchangeable. A future
SQLite-to-PostgreSQL transfer must initialize the target schema with Goose and
copy typed rows, preserving IDs, timestamps, booleans and artifact bytes. Drain
execution first; active claims are not portable running processes. Transfer
tooling is not included in this draft.

Binary payloads use `BYTEA`. PostgreSQL provides that type natively; ordinary
SQLite tables accept the declaration and preserve bound byte slices as blobs.
These migrations do not use SQLite `STRICT` tables.

CI runs the SQL storage suite against SQLite and a PostgreSQL 17 service. Run the
same checks locally with Docker:

```sh
make test-postgres-local
```

This starts a disposable PostgreSQL container, waits for readiness, and removes it
after the tests, including on failure or interruption. It uses the CI image and an
available port bound to localhost.

For an existing instance, supply its test database URL:

```sh
ATLANTIS_TEST_POSTGRES_DSN='postgresql://user:password@localhost:5432/atlantis_test?sslmode=disable' \
  make test-postgres
```

Both targets run with the race detector and disable test caching. `test-postgres`
fails if the database URL is missing or PostgreSQL is unavailable. The local target
also reuses an explicitly supplied URL instead of starting a container. Ordinary
unit tests skip PostgreSQL when the URL is unset.

PostgreSQL tests create isolated schemas and remove them afterward; the database
user needs permission to create schemas. Additional backends require their own
conformance checks, including coordination semantics.

The schema is an unreleased draft. Databases created from earlier drafts must be
recreated; the migration history is not compatible with their integer identifiers.
