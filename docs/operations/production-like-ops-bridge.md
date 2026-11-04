# Production-like Operations Bridge

## Purpose

The production-like operations bridge provides an auditable, allow-listed path
for routine RouteGate diagnostics and recovery without exposing arbitrary remote
shell execution through GitHub Actions.

The bridge reuses the existing `production-like` GitHub environment and its SSH
secrets. Secrets remain inside GitHub Actions and are never copied into issues or
operation results.

## Control surface

GitHub issue `#268` (`RouteGate Ops Bridge`) is the fixed control issue. The
workflow runs only when that exact issue is reopened and its body matches one of
the exact allow-listed requests below, or when an administrator manually uses
the workflow's typed `workflow_dispatch` choice.

### Repository transfer identity

After moving from `ikaevus/RouteGate` to the `routegate/RouteGate` GitHub
organization, `github.repository_owner` is `routegate` but issue #268 was
created by the original maintainer. The GitHub Actions job now pins the
**immutable** repository ID `1224779806` and original issue creator ID
`62504580`, in addition to the full `routegate/RouteGate` repository path,
exact issue number and title. No arbitrary issue author or triggering actor is
trusted, and ordinary issues cannot enter this workflow's privileged job.
The workflow's existing allow-list, environment approval and host-side lock
remain unchanged.

The static gate is checked by `scripts/test_ops_bridge_identity.py` in CI.
This ownership fix does **not** change the unrelated, previously pinned
`routegate-update-verified.sh` attestation trust roots; those require a
separate provenance review before any verified host-update migration.

Supported requests:

- `operation=diagnose`
- `operation=traffic-inspect` (read-only US traffic accounting state, no runtime changes)
- `operation=diagnose-sing-box`
- `operation=validate`
- `operation=restart-control-plane`
- `operation=start-manager-only` (start Manager without restarting Agent, nginx or VPN services)
- `operation=restart-sing-box`
- `operation=restart-wireguard`
- `operation=restart-hysteria2`
- `operation=restart-mtproto`
- `operation=renew-certificate`
- `operation=preflight-applied-client-settings-155`
- `operation=update-manager commit=<40-character main commit>`

`diagnose-sing-box` reports only safe runtime metadata: lifecycle state, process
exit status, installed version, config validation result, and the systemd
ExecStart command. It never exposes the sing-box configuration or raw journal
output.

The issue body is never evaluated as shell code. It is mapped through a fixed
`case` statement to one fixed operation name; the only parameter, the commit of
`update-manager`, must match `^[0-9a-f]{40}$` exactly and is accepted by no
other operation. The remote script independently
checks the same allow-list before doing anything.

After every issue-triggered run the workflow posts a sanitized result to #268
and closes the issue again, making the next reopen a distinct audited request.

## Manager-only emergency recovery

If a cancelled release leaves the Manager stopped, request `operation=start-manager-only`
through issue #268. This fixed, argument-free operation is permitted only from
trusted `main` and runs under the production-like host deployment lock.
It checks the Manager systemd unit and calls `systemctl start routegate-manager.service`
only if inactive, then requires local health HTTP 200. If the Manager is
already running it does **not** restart it. No Agent, nginx or VPN service is
restarted. After recovery, request `operation=diagnose` to confirm health,
schema and data-plane service state.

## Applied-client-settings preflight

`preflight-applied-client-settings-155` runs the read-only deployment checks of
PR #499 against the Manager database, before that update is merged. It is the
only database query the bridge runs, and it is fixed:

- the SQL is `docs/operations/applied-client-settings/preflight-schema-155.sql`
  from the trusted `main` checkout, a byte-for-byte copy of the file in commit
  `b72637cf122d4d3db0c988ebb436e87bbe00d6a9` (provenance in
  `docs/operations/applied-client-settings/OPS-PREFLIGHT.md`); the host runner
  refuses any file whose SHA-256 differs;
- neither the issue nor the dispatch input can choose SQL, a URL or a ref, and
  a `workflow_dispatch` of this operation from any ref other than `main` is
  refused;
- `scripts/production-like-preflight-applied-client-settings.sh` loads the
  connection from `/etc/routegate/manager.env` without printing it, checks
  read-only that the schema is `000155` with no `000156+` migration (and stops
  otherwise, without migrating or adapting anything), then runs the SQL with
  `psql -X`, `ON_ERROR_STOP`, no pager, `default_transaction_read_only=on`,
  statement and lock timeouts; the SQL keeps its own `READ ONLY` transaction
  ending with `ROLLBACK`;
- the host lock and the temporary files follow the other operations; both
  files are removed after the run.

The full P0–P7 output (node names and account ids, no secrets) is uploaded as
the run artifact `routegate-preflight-applied-client-settings-155-<run id>`,
kept 7 days. The Actions log and the #268 comment carry only the runner's
status lines and the artifact reference. `PASSED` means the checks completed;
their results may still block the update. Runner exit codes: 2 usage or SQL
checksum mismatch, 3 unexpected schema, 4 psql, environment or connection
unavailable (never installed or granted automatically), 5 the checks failed.

## Manager-only update

`update-manager` updates only the Manager (binary, migrations, Web UI, unit) to
one pinned commit, for the applied-client-settings rollout (schema `000155` to
`000158`). The commit must be contained in `main` and have a successful
`RouteGate CI` push run; the workflow builds the bundle from exactly that
commit, while the runner `scripts/production-like-update-manager.sh`, the
update libraries and the pinned preflight/postflight SQL come from the trusted
`main` checkout. A `workflow_dispatch` from another ref is refused.

On the host it gates on the read-only preflight, drains Agent jobs, stops the
Manager, backs up Manager files and the database, installs the new Manager,
gates on the read-only postflight and the public Web UI, and on any failure
restores the previous Manager and schema (down migrations, then `pg_restore`).
It never restarts or rewrites the Agent, VPN runtimes, nginx, observability,
the maintenance dispatch or bootstrap artifacts, creates no apply job and
contacts no other node; it verifies that their units and files are unchanged.
The full output is the artifact `routegate-update-manager-<run id>` (7 days);
the log and #268 get status lines only. Details, limits and the run sequence:
`docs/operations/applied-client-settings/MANAGER-ONLY-UPDATE.md`.

Production-like Deploy no longer starts after CI on `main`; a full platform
deploy is a manual dispatch from `main`.

## Serialization

GitHub Actions deploy jobs and ops jobs use separate concurrency groups. Cross-
workflow serialization is enforced on the production-like host itself with an
exclusive `flock` on `/run/lock/routegate-production-like.lock`.

This avoids GitHub's one-pending-run concurrency behavior silently replacing a
queued ops request with a later deploy (or vice versa), while still guaranteeing
that a mutating ops action cannot overlap a platform deployment. Both workflows
wait up to ten minutes for the host lock before failing explicitly.

## Security boundary

The bridge deliberately does **not** support:

- arbitrary shell commands;
- arbitrary service names;
- arbitrary paths or files;
- arbitrary database queries;
- arbitrary VPN configuration rollback identifiers;
- secret, environment, configuration, or journal dumps.

Diagnostics expose only service lifecycle state, HTTP status codes, the applied
schema version, and safe VPN runtime state. Raw journals are excluded because
the repository and Actions output may be visible to people who do not need
production secrets.

Mutating operations are narrow and reversible where practical. Runtime restarts
are isolated to one known service. A sing-box restart first validates the active
configuration. Control-plane restart and certificate renewal reuse the installed
`routegate-recovery` command instead of duplicating recovery logic.

## Control plane and data plane

Platform deployment requires a healthy RouteGate Manager and Agent. VPN runtimes
are independent data-plane components. A degraded sing-box, WireGuard,
Hysteria2, or MTProto runtime must be reported and recovered independently and
must not block an otherwise healthy RouteGate platform deployment.

The production-like deploy workflow therefore performs a strict preflight only
for Manager and Agent. Runtime status is diagnostic-only in the deployment
script. Platform rollback does not restart or rewrite unrelated VPN runtimes.

## Break-glass boundary

Operations that require an arbitrary VPN backup UUID or genuinely open-ended
host investigation remain break-glass procedures. They should use the local
`routegate-recovery` tool or direct administrative access rather than widening
the GitHub bridge into a general root shell.

## Publish bootstrap without deployment

`operation=publish-bootstrap commit=<40 lowercase hex>` publishes the pinned
Manager commit's packages for fresh nodes without updating existing services.
See [publication instructions](publish-node-bootstrap.md).
