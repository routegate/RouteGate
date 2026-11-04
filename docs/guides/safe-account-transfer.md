# Safe account transfer (RG-140)

Status: PR #511 merged; Manager main commit `c4137cc4` published on US.
**Live FI → US → FI client checks and both cleanups, post-cutover rollback and
failed-cleanup/retry passed on 2026-10-05. Post-publication Manager sign-in and
Hiddify iPhone connectivity on FI were reported working.**
Supported first slice: active, applied VLESS/Reality accounts on
any eligible Hybrid/VPN node pair. Other protocols fail preflight; no geography,
fixed port, node ID or relative config counter is special.

The Manager-only publication is recorded in [run 37339500807](https://github.com/routegate/RouteGate/actions/runs/37339500807): schema159, all six previously served accounts and subscription/device identities preserved. Both architecture bootstrap packages were published with public hash verification in [run 37342306383](https://github.com/routegate/RouteGate/actions/runs/37342306383). All three VPS remain; US stays Hybrid. Manager relocation is not planned.

RG-140 remains open for broader protocol, security/recovery UX acceptance and the recurring workspace account-switch test instability.

## Use the managed operation

Open **Account → Routing → Placement**, choose a destination and prepare it.
For an already deployed account the form and Automatic Selection both start the
same durable operation; they do not change placement immediately. Undeployed
accounts retain ordinary assignment. Bulk/manual API reassignment of deployed
accounts is rejected.

| Stage | Next action | What happens to access |
|---|---|---|
| Target applying | Verify target when Agent finishes | Source subscription and credentials remain authoritative |
| Target ready | Switch subscription | Exact target apply, membership, fresh authenticated Agent heartbeat and public TCP port are checked again |
| Client refresh pending | Refresh clients, connect, verify exit IP | Existing URL serves target's applied parameters; old source credentials remain |
| Client verification acknowledged | Remove source access | Source render preserves other accounts and removes only transferred membership |
| Cleanup applying | Verify completion | Operation remains reserved until Agent-confirmed cleanup and absence of account are proven |
| Apply failed | Retry failed apply or cancel before cutover | No completion banner or premature subscription switch |

The operation is persisted in PostgreSQL. Reloading or restarting Manager does
not lose its state or queue duplicate jobs.

**A long-lived `client_refresh_pending` record is intentional, not a timed-out
background copy.** Its source credentials and both node reservations remain
until a human verifies client connectivity and explicitly finishes cleanup or
restores the source through the transfer flow. Do not delete the account, edit
`completed_at` or remove reservations to unblock an unrelated operation.
Account **Settings → Danger zone** shows the active transfer and links back to
**Routing → Placement**. Deletion stays disabled while the transfer is active
or its state cannot be fetched; the PostgreSQL RG-140 guards remain the
server-side enforcement. Once safely terminal, account deletion remains an
independent irreversible operation that does *not* prove runtime revocation.
 Repeating Prepare for the same
active account/destination returns the existing operation. Tasks still executing
on Agent cannot be cancelled safely; wait for a terminal result. A pending,
unclaimed task can be cancelled under a row lock before target cleanup.

Before cutover, cancellation cleans any prepared target membership while source
remains authoritative. After cutover, rollback requires explicit acknowledgment
that clients must refresh again, and a verified retained source. **After source
cleanup starts, quick rollback is unavailable**, including when cleanup fails:
a failure is not evidence that old credentials were successfully restored.

Nodes are reserved for the operation, including configuration apply/reapply,
protocol/account runtime edits, service operations, remote updates and Manager
apply updates. Shared routing edits are conservatively reserved too. Heartbeats,
subscription retrieval and token revocation/rotation remain available. Other
nodes' configurations are not deployed by this operation.

Source and target must have no pending configuration changes. This prevents a
transfer from silently deploying another account's saved edit. Existing accounts
on a destination without an applied baseline must be deployed separately first.

When the last account leaves a VLESS node, the scoped cleanup renderer produces
an empty authenticator on the same listener: **zero UUIDs are authorized**. This
permits runtime/listener verification while removing every transferred VPN
credential; it does not stop Manager, nginx, Agent or another service. Ordinary
renders still reject accidental empty deployments. Generated cleanup configs
were checked with sing-box 1.12.8 in the isolated tests; live runtime validation
on both installed nodes remains a canary requirement.

## Client evidence and recovery

No token is created or rotated by a normal transfer. In Hiddify, v2rayN or
v2rayNG refresh the **existing subscription**, select the updated profile,
connect and verify the exit IP. No new QR code or import is required for this
operation. Exact menu labels and background refresh behavior depend on client
version; do not promise automatic adoption.

The operation snapshots the initial active device/legacy subscription inventory.
Its view retains affected devices even if a link is revoked/replaced during the
transfer and also lists newly issued active subscriptions. A request after
cutover is **retrieval evidence only**. It does not establish successful parsing,
selection or a VPN connection. Link replacement is shown separately and requires
secure re-import. Never redirect an old compromised bearer to its replacement.

Before cleanup explicitly verify affected devices. A timeout is not proof. The
UI warns after 24 hours of inactivity; reservations and fallback are retained,
not silently removed. Complete, cancel or recover the operation deliberately.
An Agent task stuck in progress needs Agent/runtime diagnosis before continuing;
never edit database state to pretend it succeeded.

Proof versions are pinned and protected while active. Terminal operation records
remain linked to their underlying account/nodes/config history; explicit removal
of that underlying history also removes the associated record. Audit events
retain the operation ID and scoped actions without subscription bearers.

## Acceptance canary: fi-test only

After reviewing CI and deploying the approved candidate, use **fi-test**, never
real users, for FI → US → FI and US → FI → US as applicable. Record for each leg:

1. Existing device/legacy token identities and canonical subscription host are
   unchanged (compare privately; never paste bearer links into logs/issues).
2. During target preparation and an injected target failure, fetching the same
   installed subscription still returns source's applied endpoint/key/port.
3. Target's exact version ID/hash, Agent apply result, account UUID membership,
   runtime/listener checks and Manager's TCP reachability check pass.
4. After cutover, the same URL returns destination's applied values. Check both
   nodes' different ports/Reality keys; version counters are local to each node.
5. On Hiddify Android/iOS and v2rayNG Android, refresh manually, reconnect and
   verify exit IP. Record background behavior separately if observed. v2rayN
   Windows can supplement, not replace, the required Android/iOS evidence.
6. Check a deliberately stale cached client before cleanup. Explicitly acknowledge
   client verification; confirm old credentials disappear only after cleanup apply.
7. Exercise rollback before cleanup and failed cleanup/retry. Verify unrelated
   accounts and Manager/HTTPS/Agent availability after every node deployment.

Do not close RG-140 until this matrix is observed and the rollout is approved.

### Observed canary results — 2026-10-05

- FI → US: target verification, cutover and source cleanup completed through
  the guided workflow. The user confirmed Hiddify iPhone, both Honor 8X client
  entries and v2rayN Windows; the Windows exit IP was 139.60.162.138.
- US → FI: the user performed the guided transfer and initiated source cleanup;
  Manager verification confirmed completion. After refreshing the existing
  subscriptions, the user confirmed the requested Windows/iPhone/Android checks.
  fi-test is now on FI. The Windows device was first enrolled on US, so its
  continuity check covers US → FI; the other installed subscriptions cover both legs.
- Before the first cutover, an incorrect VLESS listener port in the old US
  Agent report was safely rejected. The protocol-specific Agent fix (#520) was
  deployed without changing Manager/VPN configuration or account/token/device
  identities. Cancelling the old preparation and verifying target cleanup passed.
- After the reverse cleanup, a stale US client temporarily lost connectivity.
  Manager remained available; refreshing the existing subscription restored
  client connectivity and Manager access. This reinforces the required client
  verification **before** source cleanup.
- Post-cutover FI → US rollback reached `Source restored`, with placement back
  on FI and target cleanup verified.
- [Scoped cleanup failure run](https://github.com/routegate/RouteGate/actions/runs/37274642964)
  induced a real US Agent staging failure for fi-test rollback. Its live SSH step
  verified unchanged running configs/services and removed the exact temporary
  fault. Manager refused completion of the failed job; the user retried normally
  and verified `Source restored`. No database state or Agent reports were fabricated.
- The existing Windows subscription returned to 194.164.235.101:443; v2rayN
  connectivity recovered after restarting the client. After reloading the repaired
  Manager UI, the user reported phone connectivity working on FI as well; no new
  phone exit-IP screenshot was supplied for this final cycle.
- A Manager session-handling defect was found during this cycle: any failure of
  `/api/admin/me` cleared the stored sign-in, including network/server failures.
  The branch now clears sign-in only on HTTP 401. Other failures show a retry;
  a background failure preserves the mounted workspace. This is a confirmed code
  defect, but the user's specific lost-login request was not captured.
- Seven controlled session-recovery browser tests and both candidate CI runs
  passed on `ebd94d87`. The [frontend-only US publication](https://github.com/routegate/RouteGate/actions/runs/37324231784)
  verified the public index and unchanged protected files/service identities.
  The user reloaded Manager to load the repair. The live Manager binary remains
  `d2a0990e`; publishing this frontend did not upgrade the backend or schema.

## Moving Manager while keeping subscription identity

Moving Manager is a separate operational migration. Preserve all of the following:

- The canonical **PublicURL host** used by already imported `/sub/` links. Change
  its DNS address to the new host while retaining HTTPS, certificates and the
  subscription route. A preserved token with a changed hostname is not continuity.
- A consistent database backup including accounts, device IDs, token hashes,
  applied config snapshots, Agent identity/generation and transfer state.
- The existing application identity/secrets and runtime configuration. Do not
  initialize an empty database, seed new devices or regenerate link tokens.

Quiesce writes and finish or explicitly checkpoint pending operations before a
consistent backup. Restore and validate schema/build compatibility on the new
host, switch the stable public address, then verify subscriptions and authenticated
Agent heartbeats. Avoid two writable Managers against divergent copies of the
same state. DNS caching can cause temporary reachability differences; do not
promise zero interruption. Retain the backup and a defined rollback path.

A foreign Hiddify Manager subscription cannot become a RouteGate subscription
transparently without control of its old endpoint or another trusted enrollment
channel. That migration still needs guided one-time import.
