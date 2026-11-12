# ADR-0029 — Manager-derived recovery intent and safe enrollment cutover

**Status:** RG-140 Draft / pure offline pre-dispatch model, not an implemented checkpoint service. No production activation, Agent unlock or DB migration.

## Problem

The RG-140 reservation/nonce tables (000162–000165) protect a one-time recovery conversation, but their marker, receipt and witness hashes originate from reported Agent evidence. Such records may prevent replay and inconsistent state transitions; **they do not independently prove the original filesystem state**. A stolen Agent bearer can reproduce a syntactically valid claim.

ADR-0027/0028 introduce a read-only journal model and offline `fsync` storage prototype. Neither is wired to existing Agent mutation admission. A missing marker and missing receipt can therefore still be mistaken for a clean state unless enrollment and trusted pre-dispatch expectations are addressed separately.

## Decision implemented in this Draft

Introduce **only a pure Manager-side reconstruction** of the v1 `mutation-inflight.json` marker for the two recovery-capable kinds:

```json
{"schemaVersion":1,"taskId":"<canonical-job-id>","kind":"config_apply"}
```

or `kind:"vpn_core_service"`.

From independently known Manager job identity, this exact JSON is hashed using SHA-256. The field order and bytes match Agent `BeginRuntimeMutation` in Go. A shared Agent/Manager golden fixture tests the wire contract. The model refuses invalid job kinds/IDs/generation and task/agent/server identity drift.

A reported marker matching the Manager-derived expected hash is returned **only as `marker_consistent_unattested`**. This is a syntactic cross-check, not a proof that a marker exists on the host. The receipt, witness and observed process remain Agent-controlled unless independently verified. A forged valid claim may pass this comparison and explicitly remains unattested.

This package has **no HTTP route, job dispatcher call, database write, installation change, runtime mutation or clearance path**. The model input type is not a trust root: its caller must eventually obtain job identity and generation from protected Manager persistence, never from Agent JSON.

## Required future independently committed checkpoint

Before any future journal-backed mutation is actually enabled, design a **Manager-owned immutable pre-dispatch commitment** with:
- Immutable Manager job ID, kind, server ID, Agent ID and **credential generation** at dispatch, stored with the correct permission scope.
- An expected mutation marker SHA-256 computed by Manager from the job, **not inserted from an Agent evidence field**.
- An independently sourced expected candidate/config digest for config apply, when applicable; where the Manager has no independent source, explicitly mark that check unavailable instead of substituting an Agent claim.
- A new monotonic dispatch epoch and durable audit entry **committed before** dispatch. The Agent must not mutate while the checkpoint transaction is uncommitted.
- An externally anchored latest journal tip or auditable phase, acknowledging that PostgreSQL and local `fsync` cannot form a distributed atomic transaction. Lost acknowledgements and ambiguous commits must remain fenced.

The existing `vpn_runtime_recovery_reservations` ledger is created **after** successful work and contains evidence that may be claimed by Agent. Do not retrospectively rename it an independent pre-dispatch checkpoint. This Draft does not add or backfill such a table.

## Safe Agent cutover and legacy nodes

A future explicit enrollment record must distinguish:
1. **Never enrolled:** legacy Agent with existing install, permitted to keep its previously configured behavior until an operator-approved upgrade. A newly absent journal cannot be interpreted as either clean or malicious without trusted enrollment context.
2. **Enrollment prepared, not committed:** no journal-backed mutation allowed; retry/reconcile only via audited control flow, not silent fallback.
3. **Enrolled and attested policy present:** an absent, corrupt, truncated or wrong-generation journal must refuse admission, **including both old runtime files missing**.
4. **Manager/Agent divergence or unreachable Manager:** refuse any recovery operation rather than auto-unlock; maintain existing working VPN process where possible.

This transition requires a separately tested Manager enrollment service, Agent startup policy and upgrade/migration plan. No automatic upgrade or instant activation of `runtimeMutationFencingV1` is authorized by this ADR.

## Threats and limitations

- The deterministic marker is **not secret**: any Agent token holder knowing task ID/kind can recalculate it.
- Database records are not host attestation. Malicious root/Agent can forge a self-consistent local journal, and a missing externally anchored tip conceals truncation.
- Manager-only original job identity supports consistency checks, but cannot prove correct sing-box process, listener identity, live user/session revocation or absence of side effects.
- A future authorization must be tied to a fresh, lock-held local observation, an independently authorized operator decision and a crash-safe local terminal protocol. `reply_matched_unattested` is never authorization.
- This model covers only `config_apply` and `vpn_core_service` v1 marker bytes. Hysteria2, install, update and maintenance require separate protocol review.

## Acceptance

Exact-head Agent/Manager golden contract tests; Manager pure comparison negative tests, including altered kinds/identities, malformed claims and a forged self-consistent but unattested response. Run `go test -race`, full RouteGate CI and Workspace Integration. Keep the entire RG-140 stack Draft, unmerged and offline.

**No deletion of `mutation-inflight.json`, no Agent unlock, no production rollout, and no US/FI/NL VPS operations.**
