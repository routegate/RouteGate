# ADR-0035 — Dual-local-anchor enrollment genesis: offline, fail closed, not activation

**Status:** RG-140 Draft for v0.2.0. No live Agent Runner, Manager endpoint, installer, dispatch, runtime admission or recovery/unlock integration.

## Why we cannot start with a single genesis file

ADR-0031 and ADR-0034 introduced a Manager-owned **prepared_unattested** record with an operator's live super-admin authorization and audit. No Agent has been enrolled by that action. ADR-0032's read-only checks are historical and not host attestation. ADR-0033's protected job claim remains a test-only rehearsal.

A naive "new enrollment" implementation that writes only a genesis in the local journal directory allows one-sided deletion of that file to look like a brand-new empty journal. That would be an unsafe future migration rule and could reset a local recovery fence without Manager review. A local pair of continuity records can catch **one-sided loss**, but cannot resist a root attacker who controls **both**, or establish remote Manager provenance.

## This PR (offline building block)

Package-private `writeRecoveryEnrollmentGenesisOfflineV1` and `inspectRecoveryEnrollmentGenesisOfflineV1` are only callable from code/tests in `agent/internal/tasks`; no production startup/Runner code invokes them.

Both require two **already existing** owner-controlled 0700 directories: the current Agent runtime-mutation directory with an existing regular 0600 `runtime.lock`, plus a separate dedicated journal directory. Missing roots/locks are **not created**. The function rejects any preexisting mutation marker/receipt; it never removes a marker.

It acquires the real existing **runtime mutation flock first**, then the existing journal-directory flock. Before writing, it requires a completely empty journal directory. Under those locks it creates **two independent O_EXCL files** using no-follow descriptors and owner-only 0600 permissions:

1. `enrollment-continuity-anchor-v1.json` in the runtime-mutation root; write exact canonical JSON, `fsync(file)`, close, then `fsync(runtime directory)`.
2. `enrollment-genesis-v1.json` in the journal root with **identical canonical bytes**, then `fsync(file)`, close, `fsync(journal directory)`.

The two directory roots must also be **disjoint canonical absolute paths**: neither may contain the other and neither may resolve through a symlinked ancestor. The offline helper checks this before reading or writing. A shared root or path alias defeats even the limited one-sided-loss separation claimed by this prototype. This is a path-topology constraint, not a defense against privileged filesystem remounts, concurrent malicious directory replacement, or a complete host/VM rollback. Regression tests cover rejected initial writes and historical reads through aliases or nested paths.

Each file is write-once. Neither operation truncates, repairs, renames, deletes, replaces or implicitly retries any unknown partial state. An error at any persistence boundary can leave **one partial file or just the runtime anchor**; this remains refused, even if the affected bytes appear complete. On a later attempt the existing O_EXCL anchor prevents replay. Neither local file is a production migration marker, and no Agent runtime gate treats its presence as authorization.

The payload pins the Manager preparation UUID, Agent and server UUIDs, preparation-time credential generation, a fixed `prepared_unattested` phase and next *future* journal event sequence 1. It explicitly **does not** mint an enrolled status, task-specific checkpoint, HMAC, valid host witness, or trusted Manager digest. These fields are locally supplied to an offline helper; the helper does not authenticate the Manager.

The read-only preflight takes both flocks, verifies both files' regular-file metadata, owner-only permissions, single link, non-symlink path, exact JSON and matching identity bytes, and returns a domain-separated SHA-256 observation marked **Unattested**. Both locks are released before returning. A matching digest is not an enrollment certificate, trust grant or proof of sing-box process state.

The old prototype local recovery-journal parser deliberately refuses the new genesis filename instead of treating it as a valid mutation chain. A separately reviewed, versioned journal grammar is required before integrating the two.

## Limits (security-critical)

- **Loss of both roots remains undetectable locally.** An attacker controlling both files, or a full VM rollback, can recreate an internally consistent pair. An *independent committed Manager-side enrollment root* must be bound to a trusted credential generation, physical/virtual host identity, challenge nonce and a well-defined on-host lock before any protected mode activation. DB superuser or privileged host compromise cannot be solved with two SHA-256 files.
- **No atomic filesystem transaction across the two directories.** The ordering above is deliberately fail-closed for interrupted writes; it is not a distributed commit protocol. A crash after the runtime anchor but before the journal genesis requires manual review, not automatic retry.
- **No trusted Manager handoff yet.** The caller provides an expected tuple, but no authenticated transport, trustworthy device identity, operator-approved activation or independent journal anchor is implemented.
- **No real runtime gate yet.** `BeginRuntimeMutation` and all Agent job handlers are unchanged, so creation of these offline records does not start a protected mode. An eventual active mode must refuse missing/corrupt journals and prevent fallback to the legacy path for enrolled nodes. Never apply that rule indiscriminately to existing US/FI/NL hosts.
- **No terminal clearance.** No code clears a recovery hold, rotates secrets, revokes established VLESS/Hysteria2 sessions, or unlocks runtime mutation. Successful hashing does not justify any of those actions.

## Test plan

Dedicated Linux Agent `go test -race` cases verify canonical dual-file bytes, initial refusal when genesis absent, matching observation always Unattested, no legacy journal-parser acceptance, stale/wrong Manager identity and credential generation, unsafe directories and missing existing runtime lock, preexisting mutation marker, journal corruption/symlink/hardlink/unknown files, one-sided anchor deletion/tampering, dual-lock contention, concurrent writers, and failure injection after **eight** file/dir persistence boundaries (four anchor + four journal). Incomplete/ambiguous writes are never automatically cleaned up or retried.

## Next steps before implementation

1. Define a Manager-owned, durable independent root anchoring the authenticated enrollment handshake; require **two-sided attestation/identity** or an explicitly documented limited trust model.
2. Define a safe, operator-initiated canary transition from unprepared -> prepared_unattested -> pinned local genesis -> verified enrollment **without retroactively enrolling any legacy Agent**.
3. Bind the local journal lock to the current mutation lock for an actual task and to a committed **pre-dispatch** Manager checkpoint; crash-test all Manager/Agent response and file-system boundaries, including rollback of the whole VM.
4. Only after these gates are independently reviewed may an opt-in Agent dispatcher use protected claims. Preserve existing VPN traffic and production node availability throughout.

No VPS SSH, deployment, database migration, main-branch change, live Agent mutation, or automatic recovery in this PR.
