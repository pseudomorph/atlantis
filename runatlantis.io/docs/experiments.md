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
| [Durable storage](#durable-storage) | `durable-storage` | Experimental | Unreleased (Main only). | Requires `durable-storage` and structured `storage` configuration | [ADR 0005](/contributing/adr/0005-use-durable-storage-with-one-data-model.md) | [Development #7031](https://github.com/runatlantis/atlantis/issues/7031) |

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

To disable all command and lock routes, remove `api` from `experiments` and restart. Remove `api-secret` too unless a permitted drift installation still needs it. These routes return HTTP 404 without permission. When `api` is permitted but no secret is configured, the existing read-only lock listing remains available; command requests require a secret. With durable storage configured, lock listing also requires authentication.

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
This experiment implements the SQLite portion of ADR 0005: project locks,
global command locks, PR status, job history, job logs, execution claims,
cancellation, and pending plans. PostgreSQL, distributed recovery, comprehensive
webhook deduplication, and multi-instance acceptance tests remain incomplete.
Use it only for development and testing.
:::

SQLite uses versioned Goose SQL migrations and works in CGO-disabled builds.
The database owns project locks, global command locks, PR status, job records,
live job logs, pending-plan relationships, execution claims, and cancellation requests.
Artifacts can use the database or a filesystem directory.

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

Supply the usual VCS credentials and repository allowlist. See
[storage configuration](server-configuration.md#durable-storage-configuration)
for artifact backends and retention settings. Do not combine `storage` with `locking-db-type`, `share-plan-dir`,
or `enable-external-stores`.

Experiment permission alone leaves the selected backend unchanged. Configuring
`storage` without permission rejects startup. Database open or migration errors
stop startup.

### BoltDB deprecation

::: warning Deprecated
BoltDB locking (`--locking-db-type=boltdb`, the current default) is deprecated.
:::

- **Why:** [ADR 0005](/contributing/adr/0005-use-durable-storage-with-one-data-model.md)
  replaces BoltDB with one relational data model. This experiment covers what
  BoltDB does for single-instance installs.
- **Replacement:** the `durable-storage` experiment with `storage.database.type: sqlite`.
- **Migration:** follow [Enable](#enable). Drain commands, start with a fresh
  `data-dir`, and replan open PRs. BoltDB locks, PR status, and plans are not imported.
- **Deprecated in:** the first release that includes this notice. From that
  release, Atlantis logs a warning at startup when BoltDB is the locking backend.
- **Earliest removal:** no sooner than 2 minor releases and 60 days after durable
  storage graduates. Removal is a separate reviewed PR.

Redis and `--enable-external-stores` are not deprecated yet. Their replacements,
PostgreSQL for shared deployments and object storage for artifacts, are not part
of this experiment.

### Behavior and limits

- SQLite requires a 64-bit server and one Atlantis process on local or block-backed storage. NFS/EFS
  and multiple instances are unsupported. Kubernetes deployments must stop the old
  pod before starting its replacement, for example with `strategy: Recreate`.
- VCS instances, repositories, PRs, projects, and jobs share foreign-key relationships.
  Locks and status are keyed by VCS instance, so same-named repositories on
  different instances never share them. Working directories and their in-process
  locks include the VCS instance to prevent same-named repo collisions.
- Keep `data-dir` on persistent storage. It holds the database; losing it loses
  locks, PR status, job history, job logs, execution claims, pending plans, and the
  records that locate artifacts in any backend.
- Each producing plan job owns one immutable snapshot bundle on the database
  or filesystem backend. Its manifest binds plan, lock-file, initialization,
  and declared workflow files to their paths and checksums. Apply and policy checks
  use the snapshot selected at admission; restoration verifies the archive and its
  members. A lost or cleaned working directory is restored from the snapshot.
- Logs flush every 200 ms or at 64 KiB, and before job completion. Every backend
  writes live output to a journal in the database. With the database backend the
  journal is the final copy. With the filesystem backend, maintenance archives each
  sealed log as one file and deletes its journal bytes only after the archive is
  verified and recorded; a failed write keeps the journal. Readers keep their byte
  offsets across archival. A crash can lose output still in the flush buffer.
  Interrupted logs seal at their last committed byte without clearing the
  execution claim. A log write failure fails the job.
- Plans also capture the project's `.terraform.lock.hcl`. Custom workflow files
  or directories must be declared in `storage.workflow_files` to survive loss of
  a working directory. Snapshot bundles support regular files and directories;
  symlinks, hard links, state files, and provider caches are excluded. A custom
  plan workflow must declare its output files in `storage.workflow_files`; a
  successful command with no recoverable output fails the plan stage.
- Managed plan consumers initialize restored provider/backend data before apply
  or policy checks. The producing job records the exact Terraform/OpenTofu
  distribution and version; a changed version requires a new plan. Terraform
  0.14.x follows the restored dependency lock file without `-upgrade`; Terraform
  0.15+ and OpenTofu additionally use `-lockfile=readonly`. Earlier Terraform
  keeps its existing workflow behavior and requires explicitly configured init.
  Plan workflows with custom setup or init arguments require an explicit init
  step in the apply/policy workflow; argument values are not replayed from history.
- Backend initialization metadata and the project-local `TF_DATA_DIR` are bound
  to the plan. A changed data directory, external data directory, missing input,
  checksum mismatch, or unsafe initialization flag rejects restoration. Declare
  files referenced by init arguments in `storage.workflow_files`. Custom setup
  outside an initial built-in init step requires an explicit apply/policy init.
  Terraform state itself is not backed up by artifact storage.
- Job records survive artifact cleanup. Retention protects pending plans, running
  consumers, and uncertain executions.
- Every command, autoplan, unlock, and API call holds an execution claim on its
  PR or API invocation for its whole run, including hooks and publication. Each
  project command and workflow hook records a job with its issuer, trigger,
  revision, arguments, workflow step names, outcome, timestamps, and output.
  Step commands and environment values are not stored.
- `atlantis cancel` prevents queued jobs from starting; running jobs continue to
  completion while their execution claim remains held. Active-process interruption
  is deferred. PR closure invalidates pending plans. After a crash, prior claims become uncertain and block new commands
  on the affected PR or API invocation. They never permit automatic Terraform
  retries.
- Each server process gets a new instance ID, logged at startup. Claims record
  the instance that holds them and jobs record the instance that ran them. The
  server refreshes a heartbeat on its claims every 15 seconds, including while it
  drains for shutdown. A sweep marks an active claim whose heartbeat is older than
  2 minutes uncertain and seals its open logs as interrupted. An expired claim is
  never transferred to another command; it needs the stopped-work recovery below.
  With SQLite the process lock already makes every earlier claim uncertain at
  startup, so the sweep only finds claims whose own refresh stopped. These
  intervals are fixed and not configurable.
- Closing a PR cancels queued work and defers cleanup until active work stops.
  Reopening it accepts commands again once that cleanup finishes.
- Commands report claim rejection on the PR. A rejected autoplan also fails the
  plan commit status, as autoplan does when another plan holds the PR's working
  directory; it is not retried. Closure cleanup holds its own execution claim and is fenced
  by closure generation before a reopened PR can start new work. If the VCS API
  cannot verify a close event, a durable retry blocks new and queued projects
  until the current PR state can be verified. Running Terraform retains ownership.
- API calls associated with a PR preserve that PR's project reservations.
  Non-PR API calls remain synchronous. Their temporary negative identifiers are
  reserved before checkout; a collision retries or fails without reusing history.
  This experiment adds no execution-group identifiers, persistent API group
  lifecycle, retries, or group-management UI. Those API enhancements require separate work.
- Project locks survive successful apply until closure, merge, or explicit unlock.
  `disable-repo-locking` disables project locks, while execution claims remain active.

### Unfinished execution after a crash

`atlantis unlock` clears plans and project reservations only after acquiring
execution ownership. It refuses active work. After a restart, unfinished claims
are uncertain: Atlantis cannot prove that Terraform or workflow children stopped.

Independently verify that those processes have stopped, including remote work
started by custom steps, then have an authorized operator comment:

```text
atlantis unlock --confirm-stopped
```

This is an operator attestation, not automatic process detection. Recovery also
works on a claim whose heartbeat expired. It records its actor, the previous
owner and server instance, the recovering instance, and a timestamp; marks unfinished jobs interrupted;
invalidates pending plans; and acquires recovery ownership in one transaction.
Unlock then releases the PR's reservations. Job history and logs remain available. A failed
recovery write rolls back; uncertain work never permits automatic retry. If apply
may have changed infrastructure, inspect its outcome and create a fresh plan. Do
not retry the old saved plan.

For a non-PR API invocation, use the authenticated recovery endpoint instead:

```text
POST /api/jobs/{job-id}/unlock
X-Atlantis-Token: <api-secret>
Content-Type: application/json

{"confirm_stopped": true}
```

The endpoint is available only with durable storage and API or drift permission.
It resolves the existing invocation by job UUID, refuses active or newer owners,
and reconciles all projects in that invocation. Its response contains the job UUID
and released-reservation count. It exposes no execution-group identifier and never
allocates a new synthetic identity or retries Terraform. Durable command results include `job_id`; an
API-enabled authenticated `GET /api/locks` also identifies a job for each held
non-PR reservation if the original client disconnected. The shared API credential
is audited as `api-token`, not as an individually identified human. Protect that
credential as an operator recovery capability.

The UI directs non-PR reservation recovery to this endpoint and does not override
uncertain ownership. To abandon a test database instead,
stop Atlantis, verify stopped work, preserve matching database/artifact backups,
then use a fresh data directory and replan all open PRs. This does not recover
history or migrate existing plans.

### Disable, upgrade, and roll back

Stop Atlantis before backing up the database and artifact storage. For SQLite,
include WAL sidecars or use a consistent SQLite backup. Preserve filesystem
artifacts with the matching database backup.

To disable or roll back, drain commands, independently verify Terraform has stopped,
and retain matching binary, configuration, database, and artifact backups. Remove
`storage` and experiment permission when returning to a legacy configuration.
A legacy server started on a data directory that contains durable storage logs a
warning and runs on its legacy backend; it ignores the locks and plans recorded there.
Replan before applying against an empty or restored store. Changing configuration
does not migrate state.

### Durable storage graduation checklist

- [ ] Validate locks, status, jobs, claims, and artifacts against supported SQLite and PostgreSQL versions. Initial evidence: `server/core/sqlstore/store_test.go` and `store_internal_test.go` cover persistence, concurrency, ownership, constraints, and transaction rollback. `server/jobs/service_test.go` covers job binding, artifact restoration, corruption, supersession, and history preservation after invalidation.
- [ ] Test competing commands, duplicate webhooks, cancellation, PR close, and instance failure before supporting multiple instances.
- [ ] Verify artifact retention, durable logs, and restoring pending plans across restarts and PR cleanup.
- [ ] Test backup, upgrade, clean cutover, and rollback procedures for released schemas.
- [ ] Review stable configuration and document remaining operational limits.

Graduation, changing defaults, and removing BoltDB are separate reviewed changes. The experiment implements only the SQLite part of ADR 0005 and is not ready for production use. Track remaining work in [#7031](https://github.com/runatlantis/atlantis/issues/7031).
