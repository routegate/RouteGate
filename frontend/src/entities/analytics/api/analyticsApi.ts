import { apiGet, apiPost, apiPut } from '../../../shared/api/client';

export type HealthState = 'healthy' | 'degraded' | 'unhealthy' | 'unknown';

export interface AnalyticsOverview {
  generatedAt: string;
  summary: AnalyticsSummary;
  nodes: AnalyticsNode[];
  alerts: AnalyticsAlert[];
}

export interface AnalyticsSummary {
  totalNodes: number;
  healthyNodes: number;
  degradedNodes: number;
  unhealthyNodes: number;
  unknownNodes: number;
  activeAlerts: number;
  criticalAlerts: number;
  locatedNodes: number;
}

export interface AnalyticsNode {
  id: string;
  name: string;
  status: string;
  provider?: string;
  publicIp?: string;
  location: {
    label?: string;
    country?: string;
    region?: string;
    city?: string;
    latitude?: number;
    longitude?: number;
    source?: string;
  };
  agent: {
    status?: string;
    version?: string;
    lastSeenAt?: string;
    observationReceivedAt?: string;
    observationAgeSeconds?: number;
    observationFresh: boolean;
  };
  vpnCore: {
    type?: string;
    installed: boolean;
    version?: string;
    serviceState?: string;
  };
  resources: {
    load1?: number;
    logicalCpus?: number;
    memoryUsageRatio?: number;
    rootFsUsageRatio?: number;
    hostUptimeSeconds?: number;
  };
  health: {
    state: HealthState;
    reasonCode?: string;
    summary?: string;
    recommendedAction?: string;
  };
  alertCount: number;
  hasCriticalAlert: boolean;
}

export interface AnalyticsAlert {
  id: string;
  serverId: string;
  serverName: string;
  severity: 'warning' | 'critical';
  state: 'pending' | 'firing';
  summary: string;
  reasonCode?: string;
  startedAt: string;
  firingAt?: string;
  acknowledged: boolean;
}

export interface ServerGeographyInput {
  country: string;
  region: string;
  city: string;
  latitude?: number;
  longitude?: number;
  source: 'manual';
}

export interface DiagnosticRun {
  id: string;
  serverId: string;
  profileKey: string;
  status: 'queued' | 'running' | 'succeeded' | 'failed';
  state?: HealthState;
  reasonCode?: string;
  summary?: string;
  recommendedAction?: string;
  errorMessage?: string;
}

export function getAnalyticsOverview(): Promise<AnalyticsOverview> {
  return apiGet<AnalyticsOverview>('/api/v1/analytics/overview');
}

export function updateServerGeography(serverId: string, input: ServerGeographyInput): Promise<unknown> {
  return apiPut<ServerGeographyInput, unknown>(`/api/v1/servers/${serverId}/geography`, input);
}

export function autoDetectServerGeography(serverId: string): Promise<unknown> {
  return apiPost<undefined, unknown>(`/api/v1/servers/${serverId}/geography/auto-detect`);
}

export function runHostDiagnostic(serverId: string): Promise<DiagnosticRun> {
  return apiPost<{ profileKey: string }, DiagnosticRun>(`/api/v1/servers/${serverId}/diagnostics`, {
    profileKey: 'host_overview',
  });
}

export function runManagerCertificateDiagnostic(serverId: string): Promise<DiagnosticRun> {
  return apiPost<{ profileKey: string }, DiagnosticRun>(`/api/v1/servers/${serverId}/diagnostics`, {
    profileKey: 'manager_certificate',
  });
}

/** Check the actual Agent result before enabling another diagnostic for the node. */
export function getDiagnosticRun(serverId: string, runId: string): Promise<DiagnosticRun> {
  return apiGet<DiagnosticRun>(`/api/v1/servers/${serverId}/diagnostics/${runId}`);
}
