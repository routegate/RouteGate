import { ApiError } from '../../shared/api/client';
import type { TranslationKey } from '../../shared/i18n/i18n';

/** A queued Agent job is NOT yet evidence of diagnostic success. */
export function isDiagnosticActive(status: string | null | undefined): boolean {
  return status === 'queued' || status === 'running';
}

/** Never present an API enqueue failure as a failed TLS certificate check. */
export function diagnosticRequestErrorKey(error: unknown): TranslationKey {
  if (error instanceof ApiError) {
    if (error.code === 'agent_operation_busy') return 'analytics.diagnosticBusy';
    if (error.code === 'diagnostic_unavailable') return 'analytics.diagnosticUnavailable';
    if (error.status === 401 || error.status === 403) return 'analytics.diagnosticUnauthorized';
  }
  return 'analytics.diagnosticRequestFailed';
}
