# ADR-0026 — Internal Agent-local read-only recovery hold (no release)

**Status:** RG-140 Draft / test-only primitive. No runtime-unlock feature.

## Problem

An authenticated Agent observation can prove that the holder of the active Agent bearer sent an internally consistent account of a past local state, but **cannot** prove independently that state was genuine. Moreover, the public `ObserveRuntimeRecovery` returns only after releasing the Agent's exclusive mutation flock. Its historical snapshot must not authorize a later, separate operation that removes `mutation-inflight.json`.

The existing successful response `reply_matched_unattested` is intentionally **not** a recovery decision. Local release requires a new, lock-held observation, independent authorization and provenance checks, and a crash-safe, auditable transition. Simply retaining the HTTP nonce, token MAC, or earlier digest must never be enough.

## Decision

This Draft adds the **unexported** Agent-only `beginRuntimeRecoveryReadOnlyHoldV1` and the `observeRuntimeRecoveryUnderLock` internal helper, while preserving the existing public `ObserveRuntimeRecovery` semantics.

- The private hold first requires existing durable runtime-mutation state and the canonical task identity, then obtains the **same flock** used by ordinary mutation and credential-removal admission.
- Without releasing this flock, it uses the existing strict, read-only observation of the acknowledged result receipt, immutable marker bytes, active configuration, current sing-box process identity, previous generation and listener health. Evidence failures close the lock and refuse.
- Its `Close` releases the **ephemeral flock only**. It **never** deletes `mutation-inflight.json`, consumes a nonce, removes a receipt, resumes task dispatch, edits VPN configuration or terminates a session. A historical observation is untrusted after Close.
- The public observation now borrows this same primitive and closes it before returning, so its legacy contract remains unchanged. There is no new Agent Runner entry point and no network route or task type.

## Why this is NOT enough for recovery

Local flock ownership only excludes other compliant Agent mutation paths. It does not exclude external root access, an already compromised Agent, a stolen bearer, a replaced filesystem, or dishonest OS/process introspection. The saved expected hashes in the Manager ledger still originate from the bearer holder; there is **no independent attestation root**. An HTTP 202, a correct digest, or a successful local read cannot be upgraded into a `recovered` state.

Before a release protocol can even be proposed:

1. Define an independently verifiable original checkpoint/provenance and trusted operator authorization. Explicitly model host/Agent compromise and key rotation.
2. Reconcile Manager reservations/attempt epoch and Agent generation without a replay or lock gap. The Manager's one-time observation nonce is *not* an authorization token.
3. Design a crash-safe **local** transactional/phase protocol for receipt and marker handling, with immutable audit, exact rollback/restart semantics and a safe state after every interruption.
4. Prove targeted VLESS credential removal on the actual applied listener, preservation of unrelated users/protocols and termination of already established sessions, not just removal from a file.
5. Run isolated PostgreSQL + disposable sing-box tests, fault injection and security review. Only then evaluate an explicitly enabled canary; no production default activation.

## Tests and limits

Isolated Agent tests prove the fresh hold actually blocks a second flock and the existing public observation, refuses malformed/stale/cancelled evidence, releases its own lock on failure, preserves unchanged marker/receipt/config bytes, and cannot clear the durable fence even after Close. The full Agent and Manager release CI remains required.

This stage does **not** add an installer toggle, migration, auth bypass, runtime mutation, `main` merge, VPS change, or release. All RG-140 stacked PRs remain Draft.
