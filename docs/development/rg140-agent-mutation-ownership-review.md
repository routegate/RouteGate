# RG-140 Agent mutation ownership review (schema-170 integration)

Status: **review evidence only / NO-GO**. This document records the
responsibility boundaries observed in the consolidated branch. It does not
authorize dispatch, production rollout, automatic recovery or a merge.

## Single ownership of local mutation state

| Concern | Existing owner | Callers / dependencies |
| --- | --- | --- |
| Local exclusive lock and durable intent | `agent/internal/tasks/runtime_mutation.go` (`lockRuntimeMutation`, `BeginRuntimeMutation`) | `agent/internal/heartbeat/runtime_mutation.go` and `CredentialRemovalExecutor.lock` |
| Durable successful-result receipt and delivery replay | `agent/internal/tasks/runtime_mutation_result.go` | Heartbeat `withRuntimeMutation` / `replayRuntimeResult` |
| Credential-removal mutation and evidence | `agent/internal/tasks/credential_removal.go` | Uses shared `lockRuntimeMutation` and `readRemovalFile`/`writeRemovalFile`/`removeRemovalFile` |
| Manager-side candidate preparation | `backend/internal/configs/credential_removal.go` | Produces candidate only; does not prove a running service changed |
| Agent-side delta verification | `agent/internal/tasks/credential_removal_contract.go` | Independently verifies on-disk runtime before mutation |

### What is shared and what must stay independent

- Both Agent mutation paths use **the same lock implementation and directory**.
  Do not introduce a second flock or independent lock namespace.
- The local persistence helpers live in the Agent credential-removal file and
  are already reused by the runtime-mutation and replay implementations.
  Moving them into a second copy would recreate the duplication problem.
- Manager and Agent both validate removal semantics, but **this is a trust
  boundary, not redundant application logic**: the Agent must independently
  validate the received candidate against its own runtime. Do not replace the
  Agent check with trust in a Manager-provided hash.
- The heartbeat's `withRuntimeMutation` does **not** admit credential-removal
  tasks in its ordinary switch. Do not infer that a protected removal dispatch
  path is enabled by the existence of `CredentialRemovalExecutor`.
- `ReplayRuntimeResult` only redelivers a saved success and does not clear
  the durable mutation marker. An acknowledged result is not proof that the
  live runtime is safe to unlock.

## Legacy platform-update dispatch boundary: separate review required

The polled task path is `Runner.processNextTask` →
`dispatchTask` → `withRuntimeMutation` → `dispatchTaskUnfenced`.
For `platform_update`, the unfenced switch calls
`processPlatformUpdateReconciliationTask`, which explicitly routes
`PlatformUpdateOperationDispatch` to `processPlatformUpdateDispatchTask`
and ultimately `PrepareAndStartDetachedPlatformUpdate`.

**Observed difference:** with fencing enabled or persistent mutation state,
`withRuntimeMutation` rejects detached dispatch. With fencing disabled,
Manager negotiation absent, and no persistent mutation state, its early
legacy return still permits the historical platform-update dispatch path.
This is **not** evidence of a new bypass introduced by consolidation, nor
proof that the detached worker is unsafe: it is a distinct legacy operation
with its own prepared receipt and reconciliation model. Do not silently
disable it, or claim it is covered by the shared Agent mutation lock.
A release decision needs an explicit compatibility and handoff review.

The new `TestLegacyUnfencedDispatchCannotEnableDormantMutations` asserts
only that unknown and credential-removal kinds remain unsupported in that
legacy path. It deliberately does not assert that platform-update dispatch
is disabled.

## Cross-protocol race: detached worker versus shared runtime lease

**Unresolved safety gap, code-inspection finding (not an observed exploit):**

- `tasks.PrepareAndStartDetachedPlatformUpdate` checks
  `RuntimeMutationStatePresent(DefaultRuntimeMutationDir)` before preparing
  and launching the detached worker.
- `tasks.RunPlatformUpdateWorker` independently checks that same predicate
  before validating its staged bundle and advancing its own durable receipt to
  `mutation_started`.
- `tasks.BeginRuntimeMutation` uses `lockRuntimeMutation` (flock) and writes
  `mutation-inflight.json`, but the detached platform-update worker **does
  not acquire that lock**. Its check is therefore a point-in-time observation,
  not an atomic admission handshake with the shared lease.
- Potential interleaving: detached worker observes the runtime-mutation
  directory absent; another Agent task creates the directory, acquires the
  lock and begins a config/service mutation; detached worker continues toward
  its independently recorded `mutation_started` and updater invocation.
  The inverse race around Agent admission also requires review.
- The platform-update receipt's no-replace creation, monotonic transitions,
  worker probe and reconciliation prevent many *same-task* replays; they do
  **not** by themselves establish exclusion against a *different* Agent
  runtime mutation.

**Do not fix this by simply taking the shared lock for the duration of the
Agent's detached launcher:** that process exits before the actual updater
runs, leaving the same handoff gap. A safe design must cover the detached
worker's real mutation lifetime, process death and reboot, while retaining
existing receipt and reconciliation semantics. It also needs to consider
whether the updater restarts the Agent holding a lock and how that lock is
released or inherited.

Before any executable change: design and test a single cross-protocol
admission boundary with deterministic interleaving tests, plus crash and
restart cases. Do not add a second independent lock or silently turn off
legacy platform updates.

## Required follow-up before release

1. Trace every real Agent task-dispatch call site and prove credential removal
   cannot bypass the shared lock, including the legacy/flag-off paths.
2. Review crash cuts across receipt persistence, active-config replacement,
   service restart, result delivery, and acknowledgement. In particular,
   never retry an ambiguous mutation merely because the Manager redelivered
   the task.
3. Keep independent Manager/Agent validation; deduplicate only within a
   single trust boundary when there is a demonstrably identical responsibility.
4. Re-run full exact-HEAD CI, including PostgreSQL, Agent and Manager-to-Reality
   traffic. A prior HEAD passing is not a substitute for the current HEAD.
5. Do not merge, deploy to live VPS, rewrite history, or enable recovery
   dispatch on the basis of this review.

This is a **targeted audit**, not a claim that all 243 changed PR files have
been reviewed.
