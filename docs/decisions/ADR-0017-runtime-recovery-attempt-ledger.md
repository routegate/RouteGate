# ADR-0017 — Durable, observation-pending recovery attempt (RG-140)

**Status:** Experimental foundation, Draft-only. This is not a recovery
executor, proof of VPN credential revocation, unlock API or permission to
deploy RouteGate v0.2.0.

## Why another ledger is necessary

ADR-0016 guarantees that a super_admin's bound challenge commitment is
persisted and audited before the secret can be shown by a future trusted
caller. ADR-0015 guarantees that a valid presented token changes exactly
one `issued` challenge to `consumed`, with a PostgreSQL audit entry.

However, a Manager crash *after consuming* the token but *before creating
a recovery action* would otherwise lose the relationship between a
human authorization and the work that still needs to be validated.
Automatically retrying or unlocking based on a consumed challenge would
be unsafe.

## Decision

Migration `000164_runtime_recovery_attempt_ledger` adds one immutable
`vpn_runtime_recovery_attempts` record per consumed challenge, recovery
reservation and node. PostgreSQL creates it from the **same transaction**
that changes a challenge from `issued` to `consumed`. Failure to insert
the attempt aborts the entire challenge transition and its audit event.

The attempt captures:

- an irreversible Manager-side sequence (`attempt_epoch`) and UUID;
- the existing immutable reservation, challenge, node and Agent IDs;
- originating job ID/kind, Agent credential generation and three
  opaque SHA-256 evidence digests;
- authenticated human user and session identity;
- the database timestamp of challenge consumption;
- the sole initial state `awaiting_fresh_observation`.

The sequence is useful for future fencing design, but is **not yet an
Agent-verifiable fencing token**. It cannot be used to release local state
or to rerun an uncertain task. No Manager→Agent message is dispatched.

All records are append-only. UPDATE, DELETE and TRUNCATE are rejected,
including attempts that remain indefinitely waiting for observation.
A database downgrade cannot erase the history while any attempt exists.
The insertion guard compares the full record against the exact
consumed challenge and immutable reservation in PostgreSQL, preventing
fabricated or mismatched evidence rows.

The migration also backfills previously consumed challenge records,
if any, into **observation-pending** attempts; it does not treat historical
success as proof of a currently healthy Agent.

## Security boundaries

The SQL trigger cannot independently authenticate knowledge of the
challenge secret; that is performed by the internal
`recoveryauth.Consumer.Consume` method before its conditional UPDATE.
A database superuser can bypass SQL trigger protections, so normal app
credentials, migration permissions and operational access remain
separate security controls.

A consumed challenge and its newly recorded attempt do **not** prove
that the current sing-box configuration is unchanged, that the process
is the same generation, or that a disconnected user credential has
stopped working. There is no release transition, TTL-based bypass,
automatic reissue, runtime restart, Agent request or operator UI.

Before production use, the next independently reviewed layers must
bind a fresh one-time Manager challenge to a live Agent observation
under its exclusive local mutation lock, verify the Agent and process
generation, and implement a crash-safe, audited release protocol that
cannot repeat a previous VPN mutation or forget an unresolved intent.
Only then should scoped VPN credential/session revocation be accepted
end-to-end in disposable infrastructure.

CI acceptance uses isolated PostgreSQL tests for atomic consumption,
one record under concurrent callers, immutable history, fabricated
admission refusal, and rollback when the attempt cannot be written.

**No changes to running US/FI/NL/RU VPS, deployed databases, main, tags,
installer or releases are authorized by this ADR or PR.**
