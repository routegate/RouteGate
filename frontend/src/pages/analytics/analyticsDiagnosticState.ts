import type { TranslationKey } from '../../shared/i18n/i18n';

/** A queued Agent job is NOT yet evidence of diagnostic success. */
export function isDiagnosticActive(status: string | null | undefined): boolean {
  return status === 'queued' || status === 'running';
}

/** Never present an API enqueue failure as a failed TLS certificate check. */
export function diagnosticRequestErrorKey(error: unknown): TranslationKey {
  // Use bounded API error fields only; never echo server-provided text (which
  // may contain implementation details). Keep the model testable without Vite.
  if (error !== null && typeof error === 'object') {
    const failure = error as { status?: unknown; code?: unknown };
    if (failure.code === 'agent_operation_busy') return 'analytics.diagnosticBusy';
    if (failure.code === 'diagnostic_unavailable') return 'analytics.diagnosticUnavailable';
    if (failure.status === 401 || failure.status === 403) return 'analytics.diagnosticUnauthorized';
  }
  return 'analytics.diagnosticRequestFailed';
}
