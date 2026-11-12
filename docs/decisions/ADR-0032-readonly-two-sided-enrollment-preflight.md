# ADR-0032 — Historical two-sided recovery enrollment preflight, never activation

**Status:** RG-140 Draft, offline and read-only prototypes for v0.2.0. No runtime, HTTP, installer, Manager dispatcher or Agent Runner integration.

## Context

Migration 000166 stores an immutable Manager-derived pre-dispatch job checkpoint, but it is not yet coupled to Agent task claim. Migration 000167 stores immutable preparation records, always `prepared_unattested`. Neither migration enrolls a real host. ADR-0028's local recovery journal is also an offline prototype, with no installer integration or trusted genesis.

The next question is whether both sides can **inspect** their historical evidence without accidentally converting observations into permissions or affecting existing working VPN sessions.

## Decision

This Draft adds two separate, package-private, **strictly read-only** preflight helpers, which remain unconnected to any live code path.

### Manager PostgreSQL view

`inspectRecoveryEnrollmentPreflightV1(ctx, pool, agentID)` opens a **REPEATABLE READ, READ ONLY** transaction and reads a single Manager-origin snapshot:

- Current Agent identity, associated server, `runtimeMutationFencingV1` capability, current credential generation and disabled status
- Historical preparation identity, generation, exact `prepared_unattested` state and audit event count
- Number and generation of Manager pre-dispatch checkpoints, their epoch and expected marker hash
- Existing recovery reservations and outstanding Manager config, service and platform-update jobs

The result **always** has `Assessment=manual_review_required_unattested` and `Unattested=true`. Diagnostic reasons include missing preparation, stale generation, missing/inconsistent checkpoint, current Manager jobs, post-event hold and mismatched preparation audit. Crucially, the reasons **always** include `local_journal_unverified`, `independent_host_provenance_unverified` and `dispatcher_checkpoint_coupling_not_implemented`.

There is no `CanEnroll`, `CanDispatch`, `CanResume`, `CanUnlock`, `Approved` or host-attestation field. A clean-looking PostgreSQL view is *historical* the instant the read-only transaction ends, and cannot serve as a gate in a future mutable transaction. A future dispatcher must revalidate a persisted checkpoint and generation under its own lock before dispatch. This PR does not provide that gate.

### Agent offline local journal inspection

`inspectRecoveryJournalAgainstManagerExpectationV1(ctx, dir, expected)` opens only the dedicated local prototype journal directory, takes its existing nonblocking directory flock, reads all exact canonical bytes and checks its complete event chain. While the flock remains held, it compares every event's task/Agent/server ID, generation, epoch and marker hash to an expected Manager tuple. The function closes the flock and returns only an **Unattested** historical observation; malformed/empty/linked/truncated journals, stale expectations and conflicting locks are refused.

The supplied expectation is **not** authenticated by this private helper. A compromised Agent could forge a locally consistent journal and an attacker could forge the supposedly expected tuple. Even an exact byte match is not a trusted remote host witness. This helper has no network transport, Agent token, device attestation, mutation lock integration, runtime state query or unlock function.

## Important ordering gap

The current enrollment preparation and job checkpoint are intentionally independent historical records. The preparation must be committed while the Manager has no outstanding job, and the checkpoint requires a later pending job. A preflight can therefore examine both, but **that does not prove** the Agent ever saw a trusted checkpoint before modifying the runtime. Manager PostgreSQL and the Agent filesystem do not share one atomic transaction.

This Draft intentionally does **not** invent an enrollment activation transition or combine local observation with a Manager database transaction.

## Acceptance and future work

Test scenarios include existing unprepared legacy Agent, prepared-but-unattested Agent, rotated credential generation, pre-dispatch checkpoint generation drift, active Manager job, held recovery, missing or corrupt local journal, renamed identity, wrong epoch/marker, a competing local flock, and a deliberately forged consistent local chain. Every acceptable read-only result remains `Unattested`; every invalid input is refused and existing files/job states remain unchanged.

Before moving beyond offline tests:

1. Design authenticated, authorized operator enrollment with per-node opt-in, a safe legacy rollback plan and no blanket activation for US/FI/NL.
2. Bind Manager checkpoint issuance and mandatory verification to the same locked **pending → in_progress** job transaction. Never bypass this check for an opted-in Agent if the Manager checkpoint, generation, audit or identity is missing.
3. Establish a trusted Agent journal genesis and lock ordering with the real mutation flock, including crash injection before/after file/fsync, Manager commit, network response and process restart. An enrolled Agent with a missing journal must fail closed; existing legacy nodes must continue their established behavior.
4. Resolve provenance independent of a stolen Agent bearer. Where strong host attestation is unavailable, rely on a narrowly documented, manual recovery policy — never claim SHA-256 or Agent HMAC proves the running sing-box process.
5. Only then review terminal cleanup semantics, credential/session revocation across all supported protocols (including Hysteria2) and the testable manual authorization flow.

**Scope:** no schema migration, no deployment, no `main` merge, no production database writes, no VPS SSH/config changes, no runtime fence cleanup and no automatic unlock. Entire RG-140 stack stays Draft.
