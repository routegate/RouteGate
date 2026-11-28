# ADR-0016 — Issue recovery challenges only after a durable audit commit

**Status:** RG-140 experimental draft. Private issuance primitive only.
No HTTP endpoint, token delivery to a user, Agent recovery, local fence
release or VPN credential/session revocation is enabled.

## Decision

An administrator's recovery token is *secret material*. It must not be
returned before the exact reservation and human authority are revalidated
and the stored commitment and mandatory audit event are committed together.

`recoveryauth.Issuer.Issue` requires a principal supplied by **trusted,
authenticated human-session middleware**, never a user-provided identity.
It checks the explicit `runtime_recovery:authorize` permission and
`super_admin` role, then opens a PostgreSQL transaction.

While holding the immutable reservation and Agent rows under
`FOR SHARE`, the issuer checks current Agent credential generation and
persisted runtime-fencing capability. It revalidates the active operator
session, human status, role and dedicated permission inside PostgreSQL,
using the DB guard from migration 000163 to lock the relevant authority
rows until commit.

Only after those checks does it generate 32 bytes from the operating
system's cryptographic random source and construct the versioned,
domain-separated commitment from ADR-0013, bound to:

- the exact held reservation and node;
- Agent identity and current credential generation;
- supported task ID and kind;
- immutable intent, result and runtime-observation SHA-256 claims;
- active human user and authenticated session;
- a PostgreSQL-clock issuance time and 90-second expiry.

It inserts **only the two hashes**, identifiers and timestamps in the
000163 challenge ledger. The audit trigger records `issued` in the
*same transaction*. The plaintext secret is returned from the private
Go method only after `COMMIT` succeeds. If insertion, permissions,
randomness, audit writing or commit fails, the method returns no secret.

## Intentional fail-closed behavior

The schema allows only one challenge ever per immutable reservation. A
racing second issuer can never successfully commit another row. If a
transaction's commit outcome becomes ambiguous because of a network
failure, the method returns an error and **does not expose its secret**.
A possibly committed record may be stranded. Do not reissue it automatically,
expire it to clear the Agent's local marker, delete its history or
manually override the recovery reservation.

The subsequent private `Consumer.Consume` independently verifies the
exact stored commitment and human session, consumes the challenge once,
and appends a `consumed` audit record atomically. Issuance and consumption
may both pass CI without establishing any safe live-runtime release.

## Evidence and prohibitions

Disposable PostgreSQL tests prove issuance, consumer interoperability,
exactly-one winner under contention, privilege loss, wrong reservation,
audit failure rollback, and no issue after consumption. There is a focused
`-race` CI gate.

**No operational recovery is authorized here.** The next separately
reviewed stages must define an authenticated route with secure secret
delivery, fresh Agent observation and node generation fencing, explicit
operator approval semantics, crash-safe durable release attempt/audit
and full credential/session-revocation proofs. No deployed migrations or
changes to US/FI/NL/RU are part of this PR.
