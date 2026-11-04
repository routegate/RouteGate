import type { DashboardDailyTraffic } from '../../entities/dashboard/api/dashboardApi';

export type TrafficWidgetStatus = 'loading' | 'error' | 'no-data' | 'ready';
export type TrafficQueryStatus = 'pending' | 'error' | 'success';

/**
 * Never turn an unavailable aggregate into an apparent zero.
 * The Manager emits 30 placeholder daily rows even with no measurements:
 * dailyAvailable, not daily.length, establishes availability.
 */
export function resolveTrafficWidgetStatus(
  queryStatus: TrafficQueryStatus,
  dailyAvailable: boolean | undefined,
  daily: readonly DashboardDailyTraffic[] | undefined,
): TrafficWidgetStatus {
  if (queryStatus === 'pending') return 'loading';
  if (queryStatus === 'error') return 'error';
  if (dailyAvailable !== true) return 'no-data';
  if (!Array.isArray(daily) || daily.length === 0) return 'error';

  // A successful sample may really be zero. Only invalid records are errors.
  for (const item of daily) {
    if (!item || typeof item.date !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(item.date)) return 'error';
    if (![item.rxBytes, item.txBytes, item.totalBytes].every(value => Number.isFinite(value) && value >= 0)) return 'error';
  }
  return 'ready';
}
