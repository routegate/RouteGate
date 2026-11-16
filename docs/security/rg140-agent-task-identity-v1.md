# RG-140 — Strict Agent task-identity contract v1 (review only)

> **NO-GO.** No Manager HTTP route, Agent runtime handler, task-queue
> insertion, transport send, result completion or status change is added here.

## What the v1 envelope is

The new shared, **non-secret and non-executable** v1 JSON representation
connects the uniquely reserved `task_id` (schema 165) to its immutable
`claim_id` (schema 163), `operation_id`, server, Agent, Agent credential
generation, baseline version, candidate Manager configuration digest and
candidate sing-box runtime digest. It has exactly eleven required properties:

`schemaVersion`, `taskId`, `claimId`, `operationId`, `serverId`,
`agentId`, `agentCredentialGeneration`, `baselineVersionId`,
`candidateHash`, `candidateRuntimeHash`, `taskKind`.

Only `schemaVersion=1` and
`taskKind=vless_credential_removal_v1` are accepted.
All six IDs must be lowercase canonical UUIDv4, both hashes lowercase
64-character SHA-256 hex digests, and generation strictly positive.
Both Go modules independently reject absent/extra/duplicate JSON fields,
wrong types, trailing input and payloads over 4096 bytes.
The same checked-in
[`testdata/credential-removal-task-identity-v1.json`](../../testdata/credential-removal-task-identity-v1.json)
drives both suites; it contains no VLESS UUID or secret.

## What is authenticated — and what is not

Manager's **unmounted**, read-only
`revocations.AgentTaskIdentityVerifier.Match(ctx, trustedAgentTokenHash, raw)`
must be passed `agents.HashToken(rawBearer)` **from the existing Agent bearer
authentication middleware**, not a client-supplied body field.

Its PostgreSQL transaction is explicitly `READ ONLY`. It requires the
**current** matching token hash, Agent ID + node, Agent credential generation,
recent authenticated heartbeat from that generation, applied baseline,
and exact equality with all schema-165 reservation fields and the parent
claim/preparation/confirmation. A mismatch returns only `ErrPreflight`.
No match/observation row is saved.

Agent's unmounted `tasks.ParseCredentialRemovalTaskIdentityV1` and
`MatchCredentialRemovalTaskLocalIdentity` are pure validators. The future
Agent transport must supply its local authenticated registration/generation
independently; matching fields inside a received JSON document alone is
not proof of Manager origin. There is **no signature/MAC or live challenge**
in this envelope.

## Still blocked

This is **identity matching**, NOT a verified Agent execution receipt,
not a trusted success result and not proof that VLESS clients lost access.
The existing Agent runner cannot execute this new type. No worker may infer
permission to send, repeat or apply from a successfully parsed identity.
A reservation remains `identity_reserved`, the claim remains
`outcome_unknown`, and the parent remains `prepared`.

Before activation, separately review immutable claim-to-task delivery,
bearer authentication and generation rotation, a crash-safe Agent journal,
one-shot receipt origin/integrity, applied config/version confirmation,
fail-closed stale or ambiguous responses and two-user VLESS/Reality
session teardown. Legacy #745/#746 historical task verification is not
a drop-in RG-140 receipt verifier.

The staged parent PRs #806, #808, #809, #810 and this PR remain Draft.
Do not merge, deploy to US/FI/NL/RU, alter production PostgreSQL or publish
v0.2.0 until issue #545's complete release gates are met.
