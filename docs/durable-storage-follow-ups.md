# Durable storage follow-ups

This draft implements single-instance SQLite storage. ADR 0005 remains the target
contract; the experiment is incomplete and is not ready for production.

Before the SQLite implementation is ready:

- Add a per-PR recovery path, reusing `atlantis unlock` where practical. Require
  stopped work before clearing uncertain ownership, interrupt unfinished jobs,
  invalidate pending plans, and preserve unrelated project locks and history.
- Handle PR reopening so a closed record does not permanently reject commands.
- Add workflow tests for plan/apply across restart, cancellation, PR closure, and
  artifact restoration through the durable-storage experiment.

Separate PostgreSQL and multi-instance work:

- Add the PostgreSQL driver, connection configuration, migration locking, CI
  service, and disposable local test workflow.
- Add heartbeat/lease fields through a new migration. Expiry signals uncertainty;
  it must never authorize takeover while Terraform may still be running.
- Define operator recovery and failure detection for multiple instances; test
  competing commands, duplicate webhooks, cancellation, closure, and instance loss
  before advertising shared deployments.

Deprecation notices start in a separately reviewed release once the replacement
is ready for affected installations. Graduation, the default switch, and legacy
storage removal are separate changes; removal requires the ADR 0004 notice period.
