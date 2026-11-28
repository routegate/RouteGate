# ADR-0038 — Domain-separated Agent enrollment reply HMAC (offline wire contract)

**Status:** RG-140 Draft for v0.2.0. Contract and tests only; no authenticated live enrollment and no network/API/dispatcher/runtime integration.

## Motivation

ADR-0034 made a Manager-authorized preparation immutable as `prepared_unattested`.
ADR-0035 established Agent-local dual-anchor genesis persistence, but does not authenticate host origin.
ADR-0036 established Manager-side independent expected-genesis SHA-256 and cross-module JSON parity.
ADR-0037 rehearsed a durable, single-use Manager challenge in a **test-only** PostgreSQL ledger. That test currently matches a caller-supplied digest with a bearer-token check; it does **not** authenticate the entire response as a cryptographic message.

The next independently reviewable building block is an exact, versioned authenticated response format. It must not be mistaken for host attestation or silently wired into the existing test-only ledger.

## Format

Both Agent and Manager now implement the SAME fixed Go JSON struct ordering, without importing each other's modules:

```json
{
  "schemaVersion": 1,
  "attemptId": "<manager-issued UUID>",
  "managerPreparationId": "<manager-owned preparation UUID>",
  "agentId": "<manager-bound Agent UUID>",
  "serverId": "<manager-bound server UUID>",
  "agentCredentialGeneration": 1,
  "nonce": "<canonical unpadded base64url of exactly 32 bytes>",
  "localGenesisSHA256": "<lowercase SHA-256 hex of locally observed dual-anchor genesis>"
}
```

The MAC is:

```text
HMAC-SHA256(
  key = exact existing Agent bearer-token bytes,
  message = UTF8("routegate/rg140/enrollment-probe-reply/v1") || NUL ||
            json.Marshal(fixed-v1-reply-struct)
)
```

The domain is deliberately DIFFERENT from existing RG-140 observation/recovery MAC domains. UUIDs must be canonical lowercase, the 32-byte nonce canonical base64url, and the digest exactly 64 lowercase hexadecimal characters. Manager supplies a constant-time pure MAC verifier.

The shared synthetic test vector in `testdata/rg140/enrollment_probe_reply_v1.json` pins all fields and expected MAC. Independent `go test -race` Agent and Manager suites verify parity, every identity/nonce/generation/digest binding, rejection of malformed credentials and canonical encodings, and MAC tamper refusal.

## Critical scope and threat model

- **No live path**: these functions are not called by Agent Runner, HTTP, task dispatcher, installer, runtime admission, recovery or the production Manager one-shot challenge. They neither write local genesis files nor create Manager records.
- **Key possession is not host provenance**: whoever knows the existing Agent bearer token can sign fabricated local-genesis observations. HMAC does NOT prove a specific machine, trusted VM image, running Agent, completed fsync, or absence of concurrent host mutation. A stolen token also defeats this trust layer.
- **No trusted Manager origin**: a future Agent must authenticate the Manager and confirm the nonce belongs to a committed Manager challenge before responding.
- **No independent Manager enrollment anchor**: the previous test ledger is not a production migration; the HMAC response alone is never a certificate of enrollment or a persistent anti-rollback root.
- **No live authorization**: a future Manager consumer must verify a fresh human super-admin enrollment approval, the current token hash/credential generation, exact preparation/Agent/server identity, nonce hash and expiration, two independently retained local anchors, and the entire HMAC payload **in the same protected, atomic, one-use PostgreSQL consume transaction**.
- **No safety bypass**: even a correct MAC must lead to no more than `reply_matched_unattested` until host-identity limits, rollout fences and failure recovery have been reviewed. It never grants `enrolled`, `can_dispatch`, `can_unlock`, or releases a recovery hold. Existing US/FI/NL traffic must remain unchanged.

## Follow-up before activation

1. Connect this versioned reply *only to a new disposable test-only one-shot consumer* and test modified JSON, wrong MAC, wrong nonce, expired challenge, credential rotation, concurrent replies and failed audit in one DB transaction.
2. Define durable Manager enrollment evidence and an explicit canary opt-in protocol with scoped human authorization and irreversible history, independently of the disposable fixture.
3. Add the Agent-side real dual-lock file inspection only after Manager authenticity and replay semantics are verified, and only for an operator-enrolled canary. Keep the legacy path unmodified for all existing nodes.
4. Audit rollback of both Agent VM and Manager DB and define when human/manual reconciliation is required.

No changes to main, runtime VPN, production DB, VPS, or deployed Agent.
