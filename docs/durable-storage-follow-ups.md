# Durable storage follow-ups

The SQLite experiment is single-instance and development-only. Track delivery, validation, and
graduation in [#7031](https://github.com/runatlantis/atlantis/issues/7031).

Remaining work:

- PostgreSQL configuration, migration coordination, shared artifact storage, and
  competing-command, disconnect, instance-loss, and recovery acceptance tests.
  An expired heartbeat signals uncertainty; it never authorizes takeover.
- Retention query scaling: collection currently scans unpurged plan snapshots and log streams and
  checks their relationships individually. Public job history needs pagination.
- Database snapshot and log-journal size limits and release-grade backup, restore, schema-upgrade,
  hard-cutover, and rollback acceptance evidence.
- Broader custom-workflow and remote-execution restore/reconciliation coverage.

Project reservations remain separate from execution claims and working-directory
locks. Partial multi-project success is not an atomic infrastructure transaction.

Legacy storage deprecation requires a supported replacement and migration path
for each backend, including multi-instance operation for Redis. Graduation,
default changes, deprecation, and removal remain separately reviewed decisions.

Execution-group schema and API enhancements are outside this stack. Persistent
API groups, group lifecycle/retries, and group-management UI need a separate
design. Existing synchronous API calls and job-addressed recovery remain supported.
