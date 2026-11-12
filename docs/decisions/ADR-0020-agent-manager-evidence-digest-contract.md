# ADR-0020 — Exact Agent/Manager recovery evidence digest domains

**Status:** RG-140 experimental Draft; no new endpoint, signed Agent
attestation, nonce issuance, release API or deployed database migration.

## Problem

Manager's dormant reservation/attempt rows from migrations 000162–000165
contain opaque SHA-256 claims. Those fields have not been proven to
represent the exact byte strings that Agent's read-only recovery
observer checks. A syntactically correct 64-hex digest proves nothing
about its source.

In particular, an unacknowledged result receipt and the *same* receipt
after a positive Manager acknowledgement have different bytes and
different digests. Likewise, the original result-envelope JSON, active
sing-box configuration digest, persisted mutation marker, receipt and
JSON-serialized process witness are distinct hash domains. Blindly
copying an old database hash into a new nonce request could permanently
reject a healthy node or, worse, mislabel an untrusted report as
proven evidence.

## Decision: versioned and distinct SHA-256 byte contracts

The Agent's `RuntimeRecoveryEvidenceDigestsV1` maps:

1. `markerSha256` = SHA-256 of the **exact bytes** read from private
   `mutation-inflight.json` while holding Agent's exclusive mutation lock.
2. `receiptSha256` = SHA-256 of the **exact current bytes** of private
   `mutation-result.json` with `acknowledged: true`, still under that
   lock. Pre-ACK, missing/legacy/edited receipts cannot be recovery
   evidence.
3. `envelopeSha256` = SHA-256 of the **original saved success-envelope
   JSON bytes** (the receipt's raw `envelope` member). This is not the
   Manager-rendered config hash.
4. `witnessSha256` = SHA-256 of
   `json.Marshal(RuntimeRecoveryWitness)`, using the exact Agent v1
   struct field names/types/order. This is not `witness.runtimeHash`,
   which is the canonical active sing-box configuration digest.

The helper `ObserveRuntimeRecoveryEvidenceV1` delegates to the
**existing** read-only observation. It inherits marker/receipt re-reads,
task and Manager binding, process/systemd generation, configuration
integrity, health and exclusive-local-lock checks. It produces only
bounded identity and digest fields, not raw config, tokens, filesystem
paths or executed commands. It never modifies a receipt or clears an
in-flight marker.

Manager's private `recoveryobserve` package implements the
corresponding format validator and digest comparison. The existing
one-use nonce verifier now delegates its evidence field comparison to
this explicit v1 contract.

### Cross-module regression vector

A **single synthetic, credential-free** fixture lives at
`testdata/rg140/runtime_recovery_evidence_v1.json`. Independent Go
tests in both Agent and Manager load it and verify byte-exact SHA-256
values, Agent v1 JSON witness serialization, and the distinct receipt
digest **before versus after** Manager acknowledgement.

The tests fail if a future change silently alters any digest domain,
uses the pre-ACK receipt, swaps a digest with config/envelope/witness,
changes JSON field names, or accepts a mismatched task/Agent/server
identity or process generation.

## Critical trust boundary: formatting is not provenance

The shared vector proves format interoperability only. Even a valid
`RuntimeRecoveryEvidenceDigestsV1` or Manager
`MatchesEvidenceDigestsV1` result is **not** authenticated remote
attestation: a malicious or compromised party could invent internally
consistent hashes and JSON. Nor does this stage prove that the opaque
`mutation_intent_sha256`, `result_receipt_sha256`, or
`runtime_witness_sha256` fields in migrations 000162/000164/000165
came from an independently trusted source.

Consequently, this stage does **not** turn on a production nonce
issuer. Its inputs are still claims until a separately reviewed
authenticated Agent transport and trustworthy durable evidence
provenance bind the original reservation to exactly these byte
representations. A trusted Manager must independently verify current
Agent credential generation, one-use nonce issuance/consumption and
the exact held attempt, not accept an echoed nonce as a signature.

Even future authenticated *observation* would be a snapshot: the
Agent's local lock is currently released after observation. Removing
`mutation-inflight.json` would require fresh re-observation under an
exclusive lock held through a crash-safe audited release transition.
This is not implemented. Nothing here proves real VPN client
credential removal or termination of already-established sessions.

**Keep stacked PRs Draft. No Manager→Agent endpoint, releases/tags,
main merge, installer changes, production migration or changes to
US/FI/NL/RU servers are permitted by this ADR.**
