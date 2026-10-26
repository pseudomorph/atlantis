# Experiments

Experimental features may change or be removed before becoming stable. Read the feature's documentation before using it.

## Server opt-in

The `experiments` server setting records which experiments an operator accepts. It is empty by default and read at startup. Opting in does not enable a feature; its own settings control whether it runs.

Use a command-line flag:

```sh
atlantis server --experiments=drift-detection
```

Or an environment variable:

```sh
ATLANTIS_EXPERIMENTS=drift-detection atlantis server
```

Or a YAML list in the [server configuration file](server-configuration.md):

```yaml
experiments:
  - drift-detection
```

Flags and environment variables accept comma-separated names. Repeating `--experiments` appends names. Flags override environment variables, which override the configuration file. Lists from different sources are not combined. `--experiments=` clears the list.

Names are lowercase and match exactly. Unknown names, wildcard values, `all`, and empty list entries prevent startup. Duplicate names have no additional effect. Repository `atlantis.yaml` files cannot grant experiment permissions.

## Feature status

| Feature | Experiment name | Stage | Releases | Availability | Design | Tracking |
| --- | --- | --- | --- | --- | --- | --- |
| [Drift detection](api-endpoints.md#drift-detection-and-remediation-alpha) | `drift-detection` | Experimental | Legacy API: v0.45.0+. Lifecycle setting: unreleased (Main only). | Existing enable flags; experiment opt-in is optional | [ADR 0002](/contributing/adr/0002-api-enhancement-drift-detection.md) | [Lifecycle adoption #6953](https://github.com/runatlantis/atlantis/issues/6953) |

Only `drift-detection` is accepted by `--experiments`. No current release tag contains this setting.

## Drift detection

Drift detection predates this setting. Adding it to `experiments` is optional and does not change its behavior. The existing API is not gated by this setting. See the [drift API documentation](api-endpoints.md#drift-detection-and-remediation-alpha) for configuration and limitations. Drift remediation apply additionally requires `--enable-drift-remediation`.

To disable drift detection and remediation, disable both enable flags and restart Atlantis. Removing the experiment name alone does not disable either feature. When rolling back to a binary that predates `--experiments`, remove the setting from flags, environment variables, and server YAML, and follow the target release's upgrade and recovery guidance.

Results currently use in-memory storage and are lost on restart. Drift detection runs the configured plan lifecycle, including hooks and custom steps. Remediation apply requires its own enable flag and still enforces repository apply requirements. See the [API documentation](api-endpoints.md#drift-detection-and-remediation-alpha) for request formats and limits.

### Graduation checklist

- [x] Preserve existing enable flags without a mandatory new opt-in. Evidence: [experiment configuration tests](https://github.com/runatlantis/atlantis/blob/main/server/experiments_test.go).
- [ ] Review a stable API contract against the [API controller tests](https://github.com/runatlantis/atlantis/blob/main/server/controllers/api_controller_test.go) and document supported request and error formats.
- [ ] Verify persistence and restart behavior against the [drift storage tests](https://github.com/runatlantis/atlantis/blob/main/server/core/drift/storage_test.go), and document the supported retention policy.
- [ ] Review remediation authorization and execution requirements against the [remediation tests](https://github.com/runatlantis/atlantis/blob/main/server/controllers/api_remediation_executor_internal_test.go).
- [ ] Verify enable, disable, upgrade, and rollback instructions with the target release.

See the [feature lifecycle ADR](/contributing/adr/0004-govern-feature-lifecycles.md) for the lifecycle policy and graduation requirements.
