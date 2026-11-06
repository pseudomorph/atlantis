# Experiments

Experimental features may change or be removed before becoming stable. Read the feature's documentation before using it.

## Server opt-in

The `experiments` server setting grants permission to use experimental features. It is empty by default and read at startup. An experiment's HTTP routes are unavailable without its permission. Its own settings still control authentication and activation.

Atlantis refuses to start when an experimental feature's settings are enabled without the required permission or prerequisites. Errors name the setting, explain how to fix it, and link to this page. These checks apply to command-line flags, environment variables, and YAML configuration.

Use a command-line flag:

```sh
atlantis server --experiments=api,drift-detection
```

Or an environment variable:

```sh
ATLANTIS_EXPERIMENTS=api,drift-detection atlantis server
```

Or a YAML list in the [server configuration file](server-configuration.md):

```yaml
experiments:
  - api
  - drift-detection
```

Flags and environment variables accept comma-separated names. Repeating `--experiments` appends names. Flags override environment variables, which override the configuration file. Lists from different sources are not combined. `--experiments=` clears the list.

Names are lowercase and match exactly. Unknown names, wildcard values, `all`, and empty list entries prevent startup. Duplicate names have no additional effect. Repository `atlantis.yaml` files cannot grant experiment permissions.

## Feature status

| Feature | Experiment name | Stage | Releases | Availability | Design | Tracking |
| --- | --- | --- | --- | --- | --- | --- |
| [API](api-endpoints.md#main-endpoints) | `api` | Experimental | Plan/apply: v0.19.8+. Experiment gate: unreleased (Main only). | Requires `api`; authenticated commands also require `api-secret` | [ADR 0002](/contributing/adr/0002-api-enhancement-drift-detection.md) | [API development #6162](https://github.com/runatlantis/atlantis/issues/6162) |
| [Drift detection](api-endpoints.md#drift-detection-and-remediation-alpha) | `drift-detection` | Experimental | Legacy API: v0.45.0+. Experiment gate: unreleased (Main only). | Requires `drift-detection`, `api-secret`, and drift enable flags | [ADR 0002](/contributing/adr/0002-api-enhancement-drift-detection.md) | [Development #7033](https://github.com/runatlantis/atlantis/issues/7033) |
| [Durable storage](#durable-storage) | `durable-storage` | Experimental | Unreleased (Main draft only). | Requires `durable-storage` and structured `storage` configuration | [ADR 0005](/contributing/adr/0005-use-durable-storage-with-one-data-model.md) | [Development #7031](https://github.com/runatlantis/atlantis/issues/7031) |

The supported names are `api`, `drift-detection`, and `durable-storage`. API and drift detection permit separate route groups. Durable storage permits the structured storage configuration; it grants no API or drift permission. No current release tag contains this setting.

API and drift detection share ADR 0002, but have separate tracking issues and graduation criteria. Graduating either one does not make the other stable.

## Upgrading existing alpha features

::: warning New permissions required
Existing API and drift installations must add the matching experiment names when upgrading to a release containing this gate. An existing `api-secret` or enabled drift flag without permission now prevents startup. Routes without permission return HTTP 404, including the previously public lock-listing route.
:::

For an installation using both API commands and drift detection:

```yaml
experiments:
  - api
  - drift-detection
api-secret: replace-with-your-existing-secret
enable-drift-detection: true
```

Keep `enable-drift-remediation` enabled only if you intend to permit destructive remediation apply. For a drift-only installation, omit `api`; drift routes remain available while command and lock routes stay disabled. The shared `api-secret` is permitted by either experiment.

Before rolling back to a binary that predates `--experiments`, remove the experiments setting from flags, environment variables, and YAML. Older binaries continue to use their legacy API and drift settings; follow that release's recovery guidance.

## API

The `api` experiment covers the command and lock endpoints, including `POST /api/plan`, `POST /api/apply`, and `GET /api/locks`. Drift detection, remediation, and their endpoints belong to the separate [drift detection experiment](#drift-detection).

Add `api` to `experiments` to permit command and lock routes. To use authenticated command endpoints, configure [`api-secret`](server-configuration.md#api-secret) and send its value in `X-Atlantis-Token`. Permission alone does not configure authentication or enable command execution.

Request and response fields, error handling, supported providers, and non-PR execution remain experimental. Command and lock responses currently use legacy top-level bodies; drift endpoints use a different envelope. See the [API documentation](api-endpoints.md) for endpoint-specific behavior and limitations.

To disable all command and lock routes, remove `api` from `experiments` and restart. Remove `api-secret` too unless a permitted drift installation still needs it. These routes return HTTP 404 without permission. When `api` is permitted but no secret is configured, the existing read-only lock listing remains available; command requests require a secret.

### API graduation checklist

- [x] Require API permission for command and lock routes and reject settings without permission at startup. Evidence: [configuration and route tests in #7030](https://github.com/runatlantis/atlantis/pull/7030/files).
- [ ] Review the request/response contract, error serialization, HTTP statuses, and compatibility of legacy response bodies against the [command response tests](https://github.com/runatlantis/atlantis/blob/main/server/events/command/result_test.go).
- [ ] Verify authentication, repository/team authorization, and plan/apply requirements for PR and non-PR requests against the [API controller tests](https://github.com/runatlantis/atlantis/blob/main/server/controllers/api_controller_test.go).
- [ ] Verify concurrent request isolation, lock cleanup, and shutdown behavior against the [API controller tests](https://github.com/runatlantis/atlantis/blob/main/server/controllers/api_controller_test.go); resolve the contract for [lock cleanup #6956](https://github.com/runatlantis/atlantis/issues/6956) and [shutdown #6934](https://github.com/runatlantis/atlantis/issues/6934).
- [ ] Verify supported-provider documentation and enable, disable, upgrade, and rollback instructions against the [API documentation](api-endpoints.md) and target release.
- [ ] Record the API graduation decision and first stable release in [API tracker #6162](https://github.com/runatlantis/atlantis/issues/6162).

## Drift detection

Add `drift-detection` to `experiments`, configure `api-secret`, and enable `enable-drift-detection`. Missing permission or authentication configuration prevents startup when detection is enabled. Drift remediation apply additionally requires `enable-drift-remediation`. See the [drift API documentation](api-endpoints.md#drift-detection-and-remediation-alpha) for configuration and limitations.

To disable drift detection and remediation, disable both enable flags, remove `drift-detection` from `experiments`, and restart. Keep `api-secret` only if the `api` experiment still needs it. Leaving a drift flag enabled after removing its permission prevents startup.

Results currently use in-memory storage and are lost on restart. Drift detection runs the configured plan lifecycle, including hooks and custom steps. Remediation apply requires its own enable flag and still enforces repository apply requirements. See the [API documentation](api-endpoints.md#drift-detection-and-remediation-alpha) for request formats and limits.

### Drift detection graduation checklist

- [x] Require drift permission for drift routes and reject enabled drift/remediation flags without permission at startup. Evidence: [configuration and route tests in #7030](https://github.com/runatlantis/atlantis/pull/7030/files).
- [ ] Verify drift classification, project/ref/workspace identity, and partial-failure behavior against the [drift controller tests](https://github.com/runatlantis/atlantis/blob/main/server/controllers/api_controller_test.go).
- [ ] Review drift detection/status, remediation/history, and webhook contracts against the [drift controller tests](https://github.com/runatlantis/atlantis/blob/main/server/controllers/api_controller_test.go) and [drift API documentation](api-endpoints.md#drift-detection-and-remediation-alpha).
- [ ] Verify persistence and restart behavior against the [drift storage tests](https://github.com/runatlantis/atlantis/blob/main/server/core/drift/storage_test.go), and document the supported retention policy.
- [ ] Review remediation authorization and execution requirements against the [remediation tests](https://github.com/runatlantis/atlantis/blob/main/server/controllers/api_remediation_executor_internal_test.go).
- [ ] Verify enable, disable, upgrade, and rollback instructions with the target release.
- [ ] Record the graduation decision and first stable release in [development tracker #7033](https://github.com/runatlantis/atlantis/issues/7033).

See the [feature lifecycle ADR](/contributing/adr/0004-govern-feature-lifecycles.md) for the lifecycle policy and graduation requirements.

## Durable storage

::: warning Incomplete implementation
This local draft implements part of ADR 0005. Operator recovery, PR reopening,
webhook deduplication, and multi-instance acceptance tests remain incomplete.
Use it only for development and testing.
:::

SQLite and PostgreSQL use one data model and the same versioned Goose SQL
migrations. SQLite works in CGO-disabled builds. The database owns application
records, pending-plan relationships, execution claims, and cancellation requests.
Artifacts can use the database, a filesystem directory, or S3-compatible storage.

### Enable

Drain the old installation, retain its data, and use a fresh data directory.
Existing BoltDB or Redis data is not imported. Regenerate plans before applying.

```yaml
experiments: [durable-storage]
data-dir: /var/lib/atlantis-durable
storage:
  database:
    type: sqlite
  artifacts:
    type: database
```

For PostgreSQL, replace the database section with:

```yaml
storage:
  database:
    type: postgres
    dsn_env: ATLANTIS_STORAGE_DSN
```

Supply the usual VCS credentials and repository allowlist. See
[storage configuration](server-configuration.md#durable-storage-configuration)
for artifact backends, retention, and connection settings. Do not combine `storage`
with `locking-db-type` or `enable-external-stores`.

Experiment permission alone leaves the selected backend unchanged. Configuring
`storage` without permission rejects startup. Database open or migration errors
stop startup. Databases created by earlier local drafts must be recreated.

### Legacy storage deprecations

These deprecations start in the release introducing the experiment:

| Existing setting or feature | Replacement |
| --- | --- |
| BoltDB, Redis, `locking-db-type`, and all `redis-*` settings | `storage.database` |
| `enable-external-stores` and `external_stores.plan_store` (including its S3 settings) | `storage.artifacts` |
| `share-plan-dir` | `storage.artifacts.type: filesystem` and `storage.artifacts.directory` |

Startup warns about legacy backends and configured deprecated settings. Flag,
environment, and YAML forms have the same deprecation status. The existing stores
remain functional during the experiment. Removal requires graduation and the
ADR 0004 notice period: at least two stable minor releases and 60 days, whichever is longer. The first warning release is unreleased; when it ships as N, removal is no earlier than N+2 and still requires graduation. Withdrawing the experiment withdraws these deprecations.
`data-dir` and project-locking controls remain supported.

### Behavior and limits

- SQLite requires one Atlantis process on local disk. PostgreSQL is required for
  shared deployments; multi-instance operation is not yet supported by this draft.
- VCS instances, repositories, PRs, projects, and jobs share foreign-key relationships.
  Working directories include the VCS instance to prevent same-named repo collisions.
- Apply and policy checks use the artifacts bound to their pending plan job.
  Artifact inputs are checked for size and SHA-256 before restoration.
- Logs flush in chunks during execution (every 200 ms or at 64 KiB) and before job completion. Job records survive artifact cleanup; retention
  preserves artifacts referenced by pending plans or running jobs.
- `atlantis cancel` prevents queued jobs from starting; running jobs continue to
  completion while their execution claim remains held. Active-process interruption
  is deferred. PR closure also invalidates plans and waits for active work before cleanup.
  Expired claims remain uncertain; they do not permit automatic Terraform retries.
  The draft does not yet provide a supported operator reconciliation command.
- Custom workflow files must be declared in `storage.workflow_files` to survive
  loss of a working directory.
- Project locks survive successful apply until closure, merge, or explicit unlock.
  `disable-repo-locking` disables project locks, while execution claims remain active.

### Disable, upgrade, and roll back

Stop Atlantis before backing up the database and artifact storage. For SQLite,
include WAL sidecars or use a consistent SQLite backup. Preserve filesystem and
object-storage artifacts with the matching database backup.

To disable or roll back, drain commands, independently verify Terraform has stopped,
and retain matching binary, configuration, database, and artifact backups. Remove
`storage` and experiment permission when returning to a legacy configuration.
Replan before applying against an empty or restored store. Changing configuration
does not migrate state.

### Durable storage graduation checklist

- [ ] Validate locks, status, jobs, claims, and artifacts against supported SQLite and PostgreSQL versions. Initial evidence: `server/core/sqlstore/store_test.go` and `schema_internal_test.go` cover persistence, concurrency, ownership, constraints, and transaction rollback. `server/jobs/service_test.go` covers job binding, artifact restoration, corruption, supersession, and history preservation after invalidation.
- [ ] Test competing commands, duplicate webhooks, cancellation, PR close, and instance failure before supporting multiple instances.
- [ ] Verify artifact retention, durable logs, and restoring pending plans across restarts and PR cleanup.
- [ ] Test backup, upgrade, clean cutover, and rollback procedures for released schemas.
- [ ] Review stable configuration and document remaining operational limits.

Graduation, changing defaults, and retiring BoltDB or Redis are separate changes. ADR 0005 remains Proposed. This draft implements only part of its contract. Track remaining work in [#7031](https://github.com/runatlantis/atlantis/issues/7031); this draft is not ready for production use.
