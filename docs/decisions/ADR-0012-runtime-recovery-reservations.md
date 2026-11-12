# ADR-0012 — Durable reservation before runtime recovery authorization

**Status:** Experimental foundation only (RG-140, draft #748). No live route,
deployment, operator button, automatic unlock or credential-revocation claim.

## Context and threat

An Agent can retain a local `mutation-inflight.json` marker after a crash or
lost result acknowledgement. The Agent may later obtain a byte-identical
historical result from Manager and re-observe a healthy sing-box process, but
neither fact authorizes releasing the fence. In particular, another Manager
operation could queue a config change or rotate Agent credentials between
observation and proposed recovery.

A safe release therefore needs both **Manager-side durable exclusion** and
**fresh Agent-side evidence**. These are independent requirements.

## Decision: one-way reservation (first slice)

Migration `000162_runtime_recovery_reservations` introduces
`vpn_runtime_recovery_reservations`, an append-only per-node ledger.
It intentionally has no production insertion API, release method or UI.
Its fields record a node and Agent, completed supported job, Agent credential
generation, opaque SHA-256 digests of intent/receipt/witness, actor and time.
Digest values are *claims to be verified later*, not proof of an Agent runtime.
No bearer token, runtime config, VPN secret or shell output belongs in this
table.

A reservation can be inserted only while holding the Manager-update advisory
lock and the node's `servers` row lock, and only if:

1. The current Agent belongs to that node, the credential generation matches
   and its **persisted** `runtimeMutationFencingV1` capability is enabled.
2. Exactly the identified config-apply or VPN-core restart task on that
   Agent/node is stored as **succeeded with no error**.
3. There is no active transfer-node reservation, prepared revocation, pending
   or in-progress config/service task, uncertain/pending platform update, or
   active Manager application.
4. No other recovery reservation exists for the node or the same job.

The insertion also obtains a short-lived shared lock on the existing global routing tables. It uses PostgreSQL `NOWAIT` and a fail-closed conflict if another transaction is editing routing, avoiding a server-row/table-lock deadlock. This closes the race between reserving a node and concurrent global routing changes without changing normal routing writes when no recovery reservation exists.

The existing node guard from `000161` is extended to block new operations
while a hold exists: config and Agent operations, platform updates, Agent
credential replacement, account placement/protocol edits, config versions,
transfer reservation and other guarded node mutations. The global routing
and Manager update guards also refuse writes while any hold exists.
Read-only observations and ordinary Agent heartbeats remain possible.
A prepared revocation cannot start on the held node.

The ledger is **immutable**: UPDATE and DELETE fail closed. A database downgrade
refuses to remove it while any rows exist. A later audited recovery protocol
must introduce a separate, narrowly scoped *release transition*, not delete
evidence or rely on a timer, cleanup task, or manual `rm`.

## Deliberate limitations

This is **not** operator authorization. SQL integration tests may insert
reservations directly; the application has no equivalent route or scheduled
worker. The current schema does not prove the reported SHA-256 values or
freshness of an Agent observation, and it does not cover a job that never
reached Manager's succeeded state. Such jobs remain blocked and need a
separately designed uncertain-outcome recovery path.

A successfully completed VPN service task is also not evidence that a
particular account credential is removed from every currently active session.
Device-level revocation remains unsupported for shared credentials.

## Required next slices before any release

- Explicit human authentication and a dedicated least-privilege permission;
  never treat a generic Agent bearer token or a `configs:rollback` permission
  as authorization to recover a node.
- One-use, time-bounded Manager challenge bound to Agent/token generation,
  exact task, intent/receipt/witness hashes and a separate operator approval.
- Fresh read-only Agent re-observation under the exclusive local lock, with
  systemd generation, configured executable/config source, runtime hash,
  listener/health and baseline/target generation checks.
- Transactional idempotency and audit for a **release attempt**, including
  crash between Manager approval and local fence transition. A challenge replay
  or lost acknowledgement must never authorize a second mutation.
- Test a competing Manager config apply, account transfer, Agent token rotation,
  revocation preparation, Manager update and Agent process crash in disposable
  PostgreSQL/systemd environments. Keep FI/US/NL/RU untouched.

**No operational recovery is enabled by ADR-0012 or migration 000162.**
