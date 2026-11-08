# RG-140 atomic dispatch boundary (design, not execution)

Status: proposed; based on draft #630 at e921dfe6ce8bf0b034ff4ce67fa2500b22cc59d2.
Issue #545 remains OPEN. This document does not authorize production execution.

Integration update: #631 adds an Agent-only typed delivery envelope and #632
adds dormant staging. Both are included in the integration candidate alongside
#633's regression. They do not create or claim an executable removal job.
The [lifecycle completion contract](../decisions/rg140-lifecycle-completion-contract.md)
now governs the next implementation sequence, recovery ownership, expiry and
retired-credential invariants. The observations below remain the rationale for
keeping the generic dispatch path closed.

## Observed contracts

- Migration 160's rg141_version, rg141_config_job and rg141_server call the
  reserved-node fence. It rejects INSERT, UPDATE and DELETE as applicable,
  including the version counter. An approval does not release that reservation.
- Migration 173 allocates immutable candidate/version and job UUIDs, not rows.
  Migration 174 records immutable human authorization, not a consumed dispatch.
- config_versions has UNIQUE(server_id, version). configs/repository.go locks
  the server, reads next_config_version and advances it in the insertion
  transaction. The new path must retain that serialization.
- config_apply_jobs.config_version_id is a foreign key. The old exact-ID test
  used the baseline version to isolate the job-ID fence. The added case uses
  BOTH proposed IDs and requires P0141, not a foreign-key error (23503).
- agents/repository.go ClaimNextConfigTask changes pending to in_progress;
  CompleteConfigTask changes the job again. Those writes also encounter rg141.
  A special INSERT exception alone cannot implement the lifecycle.
- The current claim RETURNING projection supplies rendered_config and
  config_hash but does not itself return request_payload. A marker stored only
  in request_payload cannot establish the Agent's scoped removal contract.
- install.sh configures routegate as database owner. There is no migration
  establishing a separate least-privilege dispatch executor. SECURITY DEFINER
  alone is not a security boundary against the same database owner.

## Minimum proposed transaction

1. Inspect and recompute the exact one-account removal candidate, then acquire
   server, preparation, Agent, human session and RBAC locks in the established
   order. Invoke validatePreparedRemovalDispatchLocked in the writer transaction.
   Re-read all mutable facts after waits; recheck the deadline at the final
   SQL write boundary. A successful earlier Check call is never authority.
2. Enter a dedicated SQL operation that consumes the bound human authorization
   once. Preserve the approval row as history; record consumption separately.
   Bind operation, actor/session, Agent generation, both UUIDs, both candidate
   hashes, original baseline and the exact next version number.
3. Insert the exact immutable candidate, advance the counter once and create
   the exact job in that same transaction. Any error rolls back all three
   effects and consumption. A repeat may read a matching receipt, but must
   never issue another job or silently regenerate IDs.
4. Preserve the node reservation through dispatch and evidence collection.
   Cancelling preparation or temporarily removing the fence to use ordinary
   Apply is not a permissible transition.
5. Keep executable rows unavailable to generic polling until the scoped claim,
   completion and Agent task contract are implemented. An implementation that
   emits a generic pending apply job before this boundary exists is incomplete.

## Unresolved implementation boundary

Before changing migration 160 guards, choose and test an unforgeable entry to
the dedicated SQL transition under the actual Manager database privileges.
Neither a caller-set GUC, knowledge of the UUIDs/nonce, an unrestricted
consumption-table INSERT nor pg_trigger_depth alone establishes this entry.

A dedicated executor role/function with protected consumption records is a
candidate, but requires explicit privilege provisioning, migration ownership,
REVOKE/GRANT and upgrade tests under a non-owner application role. Do not claim
that this separation already exists or apply it to production in this slice.
Alternatively, a constrained trigger transition needs an explicit threat
boundary and proof that direct SQL cannot manufacture its execution context.
This decision blocks a safe executable dispatch change; it does not justify
a broad bypass of P0141.

### Authority boundary clarified after #645

The restricted runtime role in #639/#642 is an ordinary application role:
it still has DML on users, auth_sessions and RBAC relations. It prevents owner
DDL, but does not independently attest a human decision against malicious SQL
under that role. A SECURITY DEFINER function that trusts only those writable
rows cannot provide that stronger guarantee. Issuing another immutable receipt
would not resolve this boundary.

Before enabling the writer, specify and test which principal may originate
human approval and which may consume it. Ordinary runtime SQL must not be able
to manufacture the protected authorization/consumption context. Credential
separation and privilege reconciliation across install/update/restore remain
prerequisites; the current owner-login installer is not that boundary.
Do not silently narrow the threat model to make an implementation pass.

The signed admission rehearsal introduces a pinned Ed25519 public key and a
deployment audience. The verifier reconstructs the signed decision after the
existing server, preparation, Agent and human-authority locks. The signature
binds the exact account, baseline, candidate, job, Agent generation, actor and
session, original nonce and approval window. Its domain commits to consent for
the shared restart. A valid SQL approval without the independently signed
decision is insufficient for this check.

The signer must authenticate the human independently and protect its private
key outside ordinary Manager. The verifier key and deployment audience must
also be outside runtime database authority. The rehearsal provisions neither
side and creates no executable rows. The future protected writer must check the
signature within its write transaction, recheck freshness at consumption and
prevent reuse atomically. A passing rehearsal is not a lease.

The isolated issuance transaction now consumes a signed decision and creates
the staged candidate version and job in `held_for_revocation` status. It also
increments the reserved node's counter once. The owner-only receipt resides in
`routegate_dispatch`; ordinary Manager runtime has no grants on that schema.
The scoped SQL guards accept only the pinned writes in the receipt's own
transaction on the trusted owner login. A restored transaction ID alone
cannot authorize a runtime login. A retry with the exact proof and a live original session returns
the stored IDs. Generic polling, status changes and cancellation cannot release
or execute the held job. Empty downgrade is supported; retained issuance
blocks downgrade.

The isolated owner connection is trusted to verify the signature before
writing the receipt. The legacy database-owner serving login does not provide
that separation. This implementation has no production caller or independent
signer service. Before rollout it needs separate coordinator credentials,
scoped Agent claim/completion and a tested terminal recovery transition.

The isolated Agent exchange adds dedicated handlers for a scoped claim and a
successful result. The claim requires the original Agent generation and a
fresh authenticated heartbeat with the removal and fencing capabilities. A
repeat can return only the original job before its recorded deadline; a
heartbeat refresh cannot extend the original delivery window. A successful
report must match the held job, candidate, listener and Agent binding.
The protected schema retains a single claim and a single verified report.
Identical report retries succeed; conflicting reports are rejected.

Reports are retained as evidence, with no change to the applied configuration
or node reservation. The handlers are exercised in isolated PostgreSQL tests.
The Agent runner can use the dedicated exchange on an isolated Manager only
after both local opt-ins and explicit heartbeat acceptance. The executor
rejects a missing, elapsed or overlong deadline and checks it again after
validation and immediately before promotion. This check relies on synchronized
Manager and Agent clocks; once admitted, mutation verification proceeds to a
safe outcome even if the delivery window subsequently closes. A missing result
acknowledgement leaves the local receipt available for reconciliation.
The production router does not mount these handlers. Terminal verification
and recovery remain required before rollout.

The integrated CI scenario drives the isolated Manager HTTP handlers and
PostgreSQL-held task through the real Agent client and a disposable systemd
sing-box instance. It uses the actual baseline rendered by Manager and
verifies the original target stream terminates, its cached UUID cannot reach
the origin again and the unrelated account reconnects. It also checks the
stored report matches the exact job and candidate while the reservation
and database active-version pointer still await a terminal transition.

Use the existing lifecycle completion contract to define dispatch, claim,
result acceptance and terminal release together. Recovery attempts must retain
the original operation/reservation and prove old-owner quiescence; the separate
succeeded-job recovery preparation is not a replacement. No new ledger is
introduced by this clarification, and executable dispatch remains blocked.

The claim/completion transitions must separately bind the original Agent
generation and operation, constrain changed columns and status edges, retain
ordinary Apply denial, and serialize consistently with server-first admission.
The deadline must be specified at each authority-consuming boundary; expired
human approval must not become a reusable execution lease.

## Required tests before enabling the writer

- Existing generic SQL cases keep returning P0141 with valid approval and
  exact IDs; complete durable rows remain unchanged after each rejection.
- Wrong actor/session/generation/nonce/hash/ID, expired approval and lock-wait
  invalidation fail without allocating rows or advancing the counter.
- Concurrent dispatch, retry after commit, and lost response create at most
  one version/job/consumption. Inject failures between each write and verify
  complete rollback, including the version counter.
- Generic claim/completion cannot execute or forge the scoped operation.
  A scoped claim returns the complete validated Agent removal contract.
- Disposable sing-box proves old UUID rejection, termination of its existing
  sessions, and another account's continued usability. Shared-process restart
  can interrupt both accounts; distinguish successful reconnection from
  uninterrupted existing sessions instead of claiming zero downtime.

PostgreSQL acceptance, HTTP receipt, IDs, and post-release evidence are not
proof of VPN access termination. No US/FI/RU activity or real credentials.

### Durable retirement guard

Schema 178 adds an append-only registry under `routegate_dispatch` with the
SHA-256 digest of the binary VLESS UUID and its revocation operation. Ordinary
Manager runtime cannot write or read the registry directly. Historical config
versions stay readable. Account writes, config writes, apply and reapply jobs,
Agent claims and active-version promotion check the registry. Both the envelope
and older plain sing-box configs are checked.

The registry insert conflicts with the shared lock held by those checks through
commit. These write paths require READ COMMITTED so a waiter reads retirements
committed during the wait. Before the registry insert commits, a deferred check
rejects a UUID still attached to an account or present in an active version or
open job on any node. A downgrade refuses to erase retirement history.

This migration provides the storage and write guards. It does not issue a
retirement or release a reservation. The protected terminal transaction must
verify and bind the exact retired UUID to the Agent result, update the account
and active version, record the retirement and release the reservation together.
Backups that predate the registry and Agent-local recovery remain separate
reconciliation paths before serving VPN traffic.

### Independent verification before a terminal write

The held Agent result records the applied configuration digest and measured
service restart. An isolated read-only check now pairs that report with a
signed traffic observation for the exact operation and report digest. The
observation attests that a pre-existing stream ended, the cached credential was
rejected and either another account reconnected or the empty listener was
verified, according to the pinned candidate. A separate coordinator key signs
this observation after the Agent report is recorded. The check rejects evidence
more than two minutes old. The systemd/Reality integration fixture signs after
the actual old-stream, cached-credential and surviving-account checks pass.

The evidence check creates no durable receipt. A terminal writer still needs to
revalidate the evidence while holding its write locks, then commit the retired
identity, account delivery state, applied configuration and reservation release.
There is no production verifier or terminal write endpoint in this change.

### Isolated terminal commit

Schema 179 adds an owner-only terminal write. The coordinator takes the node
and retirement locks, then rechecks the held Agent report and independent
traffic signature. A single transaction records both proofs, revokes the
target's legacy and device subscription tokens, rotates its UUID, retires the
old UUID and promotes the candidate with the remaining accounts' existing
client settings. The terminal job has its own history status so the ordinary
Apply lifecycle skips its protocol promotion and pruning side effects. The
existing node reservation ends at commit. An exact retry reads the original
confirmation time. A conflicting active version on another node aborts the
entire transaction.

The Manager/Agent integration fixture exercises this internal coordinator
path. Production enablement still requires provisioning the independent
verifier, an operator entry point and recovery from uncertain outcomes.

### Failed restart evidence

The scoped exchange accepts a bounded `recovery_required` report for the exact
claimed operation, including a failed restart after file promotion. The Agent
submits this report even when execution returns an error. After a lost HTTP
acknowledgement, it replays the exact locally saved failure without another
restart. Manager retains that report in the existing immutable result table.
The operation keeps its node reservation and cannot pass terminal success
verification. Additional recovery observations require a separately authorized
attempt under that same operation.

### Result delivery after execution authority expires

Held-removal receipts now record their delivery origin and acknowledgement
locally. Before requesting new work, the Agent reads unacknowledged held
results under the existing nonblocking runtime lock and submits the saved
report directly. A lost acknowledgement or Agent restart therefore does not
require the expired task to be claimed again. Manager still checks the exact
operation and Agent identity when accepting late evidence.

A held receipt left in applying state by a departed worker is converted to
a durable recovery-required report after the lock can be acquired. Delivery
and acknowledgement preserve the active file and mutation fence. Older local
receipts without the held-delivery marker retain their existing explicit
task-replay path. This delivery path grants no new mutation authority.

### Fresh consent after a recorded failure

Schema 180 records append-only human recovery approvals under the original
held operation. A separate domain signature binds the failed-report digest,
original job and candidate, Agent generation, authenticated actor/session,
fresh nonce, shared-restart consent and a lifetime of at most 90 seconds.
The original dispatch signature and nonce cannot approve recovery.

The protected coordinator locks node, operation, Agent and current human
authority before validating the signature and inserting consent. The final
insert checks the current heartbeat, session and deadline again. Concurrent
identical requests return one unchanged approval. A replacement approval must
reference the expired preceding approval, retaining the chain without forks.
A retained approval prevents downgrade from erasing its history.

Approval records do not dispatch a task or release the original reservation.
The recovery execution transition still needs an Agent observation under the
shared mutation lock and a durable attempt checkpoint that excludes resumption
by an earlier worker. Those checks must be implemented before consumption of
these approvals can authorize a restart.

### Agent observation and attempt checkpoint

The local recovery observer pins the approval key, deployment audience and
Agent generation from trusted configuration. It checks the signed decision,
the acknowledged failure report and the retained mutation fence while holding
the same nonblocking runtime lock used by removal execution. A candidate file
digest and bracketed process/listener observation are recorded separately.
Failure to establish a stable process records an unverified runtime.

Each admitted attempt has a durable signed-decision checkpoint and an original
observation time. The original failure report points to the current attempt.
Replay requires the same decision and current observations; replacing an
attempt requires its signed expiry and predecessor to match. Checkpoints,
original failure evidence, active config and the mutation fence are retained.
Collection also has a monotonic context deadline.

Recovery execution must also establish that previously submitted systemd jobs
have settled. The Agent mutation lock covers the local worker; an already
submitted service-control job can outlive that worker. The observation itself
therefore cannot establish service-control quiescence.

The observation lease retains the lock for the caller and closes it explicitly.
It provides diagnostics only. Service restart, Manager consumption and terminal
confirmation must use their own checked transition while the lock is held.
The isolated tests cover lock contention, changed identity/config binding,
expiry during observation, lost-process reconstruction, predecessor expiry,
unverified runtime and unchanged original evidence. A shared signing fixture
checks the independently built Manager and Agent contracts.
