# ADR-0027 — Recovery journal state model and independent provenance boundary

**Status:** RG-140 Draft, model-only proposal for RouteGate v0.2.0. **No disk writer, runtime-unlock path or production activation.**

## Context

PRs #754–#764 established read-only Agent/Manager observations, current bearer-bound responses, atomic Manager nonce audit, and a fresh Agent-local review while holding the shared mutation flock. This is necessary evidence, **not** authority to restore mutation admission.

Two durable Agent files are insufficient to distinguish every crash state:
- `mutation-inflight.json` records an intent; a missing file may mean either safe completion or loss.
- `mutation-result.json` records a delivery result and an ACK rewrite; its absence may also mean either cleanup or loss.
- The local `runtime.lock` serializes cooperating processes but ceases to be a durable witness after its flock is released.
- Agent bearer HMAC proves token possession, not truthful process state. SHA-256 over local files proves only byte equivalence; it is not a trusted signature.

In particular, **loss of both intent and result may look like a fresh Agent** unless a separately enrolled persistent policy and external checkpoint are consulted at admission time.

## Decision for this Draft

Introduce only a pure, package-private `recoveryJournalEventV1` format and `reviewRecoveryJournalChainV1` model with tests. There are **no** network callers, disk writes, readers in the Agent runner, imports into Manager or changes to mutation admission.

The model uses a domain-separated SHA-256 link from each event to the preceding event; it checks exact sequence/phase, immutable Attempt/Task/Agent/Server/epoch/generation, marker digest, and the difference between pre- and post-ACK receipt bytes. It returns a result **always marked `Unattested`**. An internally valid chain never means `CanUnlock`.

### Hypothetical phases (all remain fenced)

| Sequence | Phase | Meaning after a crash |
| --- | --- | --- |
| 1 | `intent_prepared` | Original intent reference exists; mutation outcome unknown |
| 2 | `mutation_uncertain` | Mutation may have started or finished; must not retry |
| 3 | `result_persisted` | Result file saved; delivery/ACK still unknown |
| 4 | `receipt_acknowledged` | Receipt bytes rewritten after ACK; actual runtime still requires fresh verification |
| 5 | `review_requested` | An observation may be examined; this is *not* operator authorization |

A valid prefix represents a possible interruption. A gap, reordering, duplicate sequence, identity drift, wrong hash, ACK without a receipt-byte change, or an unknown phase is refused. A `release_committed` event deliberately does not exist in this model.

## Non-negotiable trust boundary

A chain secured only by SHA-256 is not tamper-resistant against a malicious root user or compromised Agent: an attacker can rewrite the entire chain and recalculate hashes. Tail truncation is also indistinguishable from an earlier genuine crash if no external tip was retained.

**Independent provenance** therefore requires a separately trusted source, not another Agent-supplied digest. A future design must define and threat-model at least:
1. Manager-owned, append-only, transactionally committed **attempt/epoch checkpoint** that is written before Agent mutation is allowed; bind the exact initial intent, target digest, nodes, credential generation and operator authorization scope. A Manager database record created solely from an Agent assertion is NOT host attestation.
2. Agent installation/enrollment state: a node that previously supported runtime fencing must reject unexplained missing/corrupt journal state, including both old files missing. Legacy nodes cannot be silently interpreted as fresh clean installations.
3. Explicit operator/review authority, independently verified with fresh authentication and scoped to one attempt. Distinguish human approval from actual on-host truth; approvals alone never validate a runtime process.
4. An independently verifiable host/process evidence source or a narrower **manual recovery policy** when strong host attestation is unavailable. Do not invent a trusted hardware root where none exists.
5. A crash-safe release protocol, with strict persisted phase ordering, directory `fsync`, immutable Manager audit and no time-of-check/time-of-use gap between a fresh lock-held Agent observation and any future local release action. PostgreSQL and local disk cannot form an atomic transaction; interruption at either side must remain safe.
6. Exact semantics for all VPN credentials, active connections, routing rules, VLESS and Hysteria2; unknown protocol/state must retain the fence. Model already covers only the current VLESS/sing-box subset.

## Future implementation sequence (not implemented here)

- Design enrollment and legacy-state migration so an active US/FI/NL deployment is never accidentally locked out. No installation default changes without a separately tested cutover plan.
- Build an isolated durable writer/reader with fault injection after each write, rename, file `fsync`, directory `fsync`, ACK, Manager commit and network response loss. Test the loss-of-both-files case *against the independent enrolled checkpoint*.
- Prove the independent Manager checkpoint is committed and audit-visible before Agent can mutate; do not reuse an already spent observation nonce as an approval.
- Design terminal/clearance semantics separately. Until then, **every modeled state requires recovery review and preserves the durable fence**.
- Add integration suites with disposable PostgreSQL and sing-box, deliberate process kills, symlinks, permission failures, host identity rotations and competing operations.
- Only after security acceptance, create an opt-in canary plan. No `main` merge, direct VPS action, or automatic unlock in this ADR.

## Test evidence and limitations

Unit tests check every valid crash prefix remains `Unattested`; missing/corrupt entries, replay, reordering, phase spoofing and identity/generation drift fail closed. One deliberately forged but internally consistent chain is accepted as *syntactically valid* yet explicitly remains `Unattested`. This prevents confusing digest integrity with trust. It does **not** prove real disk durability or protection against malicious storage rewriting.

Current PR acceptance: exact-head Agent `go test -race`, full RouteGate CI, Workspace Integration; preserve Draft branch and no production action.
