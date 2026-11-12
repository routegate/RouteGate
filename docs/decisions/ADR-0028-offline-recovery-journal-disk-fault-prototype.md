# ADR-0028 — Offline append-only recovery journal disk prototype

**Status:** RG-140 Draft / fault-injection prototype only. There is no Agent Runner caller, Manager bridge, production path, installation toggle or recovery-unlock mechanism.

## Why this stage exists

ADR-0027 models local recovery states, but an in-memory slice cannot prove how a persisted chain behaves around system calls and interruptions. This PR tests the Linux persistence boundary without introducing a new production data layout or allowing historical evidence to clear a durable mutation fence.

The two existing Agent files (mutation intent and acknowledged result) do not establish a separately anchored journal. A local SHA-256 chain does not authenticate the host or prevent a privileged attacker from rewriting the history.

## Prototype decision

The package-private disk helper operates **only on an explicitly supplied, already existing, owner-only 0700 directory** (all tests use a disposable directory). It does not create that directory or derive it from DefaultRuntimeMutationDir.

Each record is stored as an unambiguous canonical JSON byte sequence in an exclusive, non-overwriting, 0600 regular file named event-NNNNNN.json. The helper:
1. Opens the directory without following a symlink, checks its owner and mode, and obtains a nonblocking exclusive directory flock.
2. Reads *all* existing entries under that lock; refuses unknown names, holes, oversized or malformed records, extra fields, duplicate keys, links, wrong permissions and any broken chain.
3. Validates the exact next phase and immutable attempt/Agent/server/credential binding using ADR-0027. It does not skip corrupted records or rewrite earlier entries.
4. Creates the next numbered file using O_CREAT | O_EXCL | O_NOFOLLOW, writes its canonical bytes, fsyncs that file, closes it, and fsyncs its containing directory before reporting success.
5. Releases the directory lock. **No data is deleted or automatically repaired** on any error.

A direct write can leave an empty or truncated file if interrupted before write/fsync completes. The next read refuses that state permanently until an explicit, separately designed reconciliation procedure exists. A complete-looking record surviving an interruption is still only locally **Unattested**.

The helper is intentionally private and not hooked into any runtime admission, task dispatcher, network transport or Agent startup. Importantly, an Agent whose intent and receipt both disappeared is still not protected by this prototype, because nothing currently consults it during admission.

## Fault-injection coverage and interpretation

Tests inject errors after file creation, after writing, after file fsync and after directory fsync. They assert: no automatic retry/overwrite, partial entry fail-closed, and parseable ambiguous entry reported as Unattested. Additional cases cover symlinks/hardlinks, public permissions, missing/untrusted directories, unknown names, gaps, duplicate JSON keys, tampering of a preceding record, concurrent append (one winner), and removal of the final entry.

These are **deterministic syscall-boundary simulations**, not a true power-loss test. They do not prove filesystem/hardware flush semantics, detect valid tail truncation or provide a transaction shared between PostgreSQL and the Agent filesystem.

## Remaining barriers before a real journal

- **Independent Manager checkpoint**: Manager must durably record the initial attempt and exact expected state *before* authorizing an Agent mutation; a later hash supplied by the same Agent is insufficient. Append-only event/tip anchoring requires a Manager-owned proof scheme and explicit operator scope.
- **Enrollment/admission cutover**: future Agent startup and runtime admission must distinguish a node that has never been enrolled from an enrolled node whose journal disappeared. Silent legacy fallback would defeat crash protection. A safe migration plan is required before touching existing US/FI/NL nodes.
- **Coordinated lock and storage path**: directory flock used in this isolated prototype is not the existing runtime mutation flock. They would need a reviewed, continuous lock ordering, including credential removals, task dispatch and crash handling. No such integration exists today.
- **Audit and replay**: Manager must treat its PostgreSQL commit independently from Agent disk acknowledgement; lost responses, restart, nonce rotation, credential generation changes, race conditions, and unknown outcomes must remain blocked.
- **Crash-safe terminal protocol**: design an independently authorized, reviewable terminal transition (including disk flush and Manager audit), followed by a fresh lock-held local check. The prototype intentionally defines no terminal release event.
- **Real-world validation**: isolated PostgreSQL and disposable sing-box network tests, process-kill tests, permissions faults, non-cooperating processes and actual reboot/power-fault exercises. No production canary before passing these gates.

All files in this PR are a test-only foundation. **Do not use the local journal as proof that it is safe to delete mutation-inflight.json or to restore VPN mutation admission.**
