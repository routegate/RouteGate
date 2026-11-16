# RG-140 — Dormant Manager verification of Agent VLESS-removal evidence

> **Review-only / NO-GO.** This package is not connected to a Manager API,
> a dispatch worker or a production Agent. It cannot remove a credential.

This review slice, based on [#741](https://github.com/routegate/RouteGate/pull/741),
carries only `backend/internal/revocations`' **v1 JSON contract, canonical
runtime digest and success-evidence verifier** onto the immutable-history
schema-161 review [#801](https://github.com/routegate/RouteGate/pull/801).
No old Manager reservation service, route handlers or Agent dispatch code
is forwarded here.

## Evidence decision and fail-closed conditions

`VerifySuccess(report, binding)` accepts only a schema-v1 success report
whose **independent trusted** `EvidenceBinding` matches the durable
operation's job, operation, account, server, Agent, baseline version,
new version, configuration hash, previous runtime hash and expected new
runtime hash. It also requires:

- bounded, duplicate-key-free and unknown-field-free JSON without
  trailing material or embedded configuration/credential data;
- valid identifier and SHA-256 digest representations;
- exactly the expected listener port and the fixed
  `shared_process_restart_observed` session-effect code;
- valid distinct systemd invocation IDs, positive PIDs and strictly
  increasing `ExecMainStartTimestampMonotonic` values;
- no success claim if the Agent reported `failed`, `recovery_required`,
  missing data or any altered operation identity.

The shared `testdata/credential-removal-v1.json` vectors pin canonical
runtime JSON and the SHA-256 digest used by the dormant Agent package
(#737) and this Manager verifier. They do **not** verify a digital
signature or attest a remote system.

## Trust boundary — still not implemented

This verifier is a **necessary but insufficient** acceptance gate.
The calling code must obtain `EvidenceBinding` exclusively from
authorized, durable Manager state; authenticate and bind the response to
the *current registered Agent generation*; ensure no replay, stale
dispatch, parallel transfer or already-applied configuration; and
persist verification under a transaction that also checks actual
applied node membership. A caller must **never** populate the binding
from the untrusted JSON being verified.

The Agent-side direct executor (#737) is currently dormant and not
connected to the live job runner. The schema-161 ledger (#801) only
reserves intent. Future review must separately implement authenticated
Manager admission/authorization, an explicit user-confirmed shared
process restart, task delivery, authenticated result handling,
idempotent recovery after Manager/Agent restart, failed apply
reconciliation and real session/traffic acceptance. Even a correctly
formed report cannot by itself prove that all remote client sessions
were terminated. In particular, a revoked `/sub/` bearer or account
status does **not** revoke cached VLESS credentials.

Never deploy to US/FI/NL/RU, promote schema 161, or claim v0.2.0 ready
without independent review and issue
[#545](https://github.com/routegate/RouteGate/issues/545) acceptance.
