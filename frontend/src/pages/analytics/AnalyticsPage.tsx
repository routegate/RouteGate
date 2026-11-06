import { groupConnections } from '../../entities/connection/model/groupConnections';
import { FormEvent, useEffect, useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from 'react-router-dom';
import {
  autoDetectServerGeography,
  getAnalyticsOverview,
  getDiagnosticRun,
  runHostDiagnostic,
  runManagerCertificateDiagnostic,
  updateServerGeography,
  type AnalyticsAlert,
  type AnalyticsNode,
  type DiagnosticRun,
  type HealthState,
  type ServerGeographyInput,
} from '../../entities/analytics/api/analyticsApi';
import { getClientConnections, type ClientConnection } from '../../entities/connection/api/connectionApi';
import { getCurrentLocale, t, type TranslationKey } from '../../shared/i18n/i18n';
import { AnalyticsWorldMap } from './AnalyticsWorldMap';
import { diagnosticRequestErrorKey, isDiagnosticActive } from './analyticsDiagnosticState';
import './AnalyticsPage.css';
import './AnalyticsMapInspectorLatch.css';

const HEALTH_RANK: Record<HealthState, number> = {
  healthy: 1,
  unknown: 2,
  degraded: 3,
  unhealthy: 4,
};

function healthLabel(state: HealthState): string {
  switch (state) {
    case 'healthy':
      return t('analytics.healthy');
    case 'degraded':
      return t('analytics.degraded');
    case 'unhealthy':
      return t('analytics.unhealthy');
    default:
      return t('analytics.unknown');
  }
}

function percent(value?: number): string {
  if (value === undefined || !Number.isFinite(value)) {
    return '—';
  }
  return `${Math.round(value * 100)}%`;
}

function numberValue(value?: number, digits = 2): string {
  if (value === undefined || !Number.isFinite(value)) {
    return '—';
  }
  return value.toFixed(digits).replace(/\.00$/, '');
}

function formatLocation(node: AnalyticsNode): string {
  const structured = [node.location.city, node.location.region, node.location.country].filter(Boolean);
  return structured.length > 0 ? structured.join(', ') : node.location.label?.trim() || t('analytics.notLocated');
}

function formatDateTime(value?: string): string {
  if (!value) {
    return '—';
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return new Intl.DateTimeFormat(getCurrentLocale() === 'ru' ? 'ru-RU' : 'en-US', {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(date);
}

function formatUpdatedTime(value?: string): string {
  if (!value) {
    return '—';
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return new Intl.DateTimeFormat(getCurrentLocale() === 'ru' ? 'ru-RU' : 'en-US', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).format(date);
}

function actionLabel(action?: string): string {
  if (!action) {
    return '';
  }
  const key = `analytics.action.${action}` as TranslationKey;
  const translated = t(key);
  if (translated !== key) {
    return translated;
  }
  return action.replaceAll('_', ' ').replace(/^./, (value) => value.toUpperCase());
}

function translatedRuntimeValue(prefix: string, value?: string): string {
  const normalized = value?.trim().toLowerCase();
  if (!normalized) {
    return '—';
  }
  const key = `${prefix}.${normalized}` as TranslationKey;
  const translated = t(key);
  return translated !== key ? translated : value!.trim();
}

function agentStatusLabel(value?: string): string {
  return translatedRuntimeValue('analytics.agentStatus', value);
}

function vpnCoreServiceStateLabel(value?: string): string {
  return translatedRuntimeValue('analytics.vpnCoreState', value);
}

function telemetryAgeLabel(value?: number): string {
  if (value === undefined || !Number.isFinite(value)) {
    return '—';
  }
  return t('analytics.secondsShort', { value: Math.round(value) });
}

function healthSummaryLabel(reasonCode?: string, fallback?: string): string {
  const normalized = reasonCode?.trim().toLowerCase();
  if (normalized) {
    const key = `analytics.healthReason.${normalized}` as TranslationKey;
    const translated = t(key);
    if (translated !== key) {
      return translated;
    }
  }
  return fallback?.trim() || '—';
}

function SummaryCard({ label, value, state }: { label: string; value: number; state?: HealthState | 'critical' }) {
  const neutralWhenEmpty = state === 'degraded' || state === 'unhealthy' || state === 'critical';
  const emphasizedState = state && (!neutralWhenEmpty || value > 0) ? state : undefined;

  return (
    <article className={`analytics-summary-card${emphasizedState ? ` analytics-summary-card--${emphasizedState}` : ''}`}>
      <span>{label}</span>
      <strong>{value}</strong>
    </article>
  );
}

function HealthPill({ state }: { state: HealthState }) {
  return (
    <span className={`analytics-health-pill analytics-health-pill--${state}`}>
      <i aria-hidden="true" />
      {healthLabel(state)}
    </span>
  );
}

function ConnectionStatePill({ connection }: { connection: ClientConnection }) {
  const exact = connection.state === 'online' && connection.confidence === 'exact';
  return (
    <span className={`analytics-connection-state analytics-connection-state--${exact ? 'online' : 'recent'}`}>
      <i aria-hidden="true" />
      {exact ? t('analytics.connectionsOnline') : t('analytics.connectionsRecent')}
    </span>
  );
}

function MapInspectorLatchIcon({ collapsed }: { collapsed: boolean }) {
  return (
    <svg aria-hidden="true" viewBox="0 0 24 24">
      <path d={collapsed ? 'M15 5l-6 7 6 7' : 'M9 5l6 7-6 7'} />
    </svg>
  );
}

function LocationEditor({ node, onClose }: { node: AnalyticsNode; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [country, setCountry] = useState(node.location.country ?? '');
  const [region, setRegion] = useState(node.location.region ?? '');
  const [city, setCity] = useState(node.location.city ?? '');
  const [latitude, setLatitude] = useState(node.location.latitude?.toString() ?? '');
  const [longitude, setLongitude] = useState(node.location.longitude?.toString() ?? '');

  const mutation = useMutation({
    mutationFn: (input: ServerGeographyInput) => updateServerGeography(node.id, input),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['analytics-overview'] });
      onClose();
    },
  });

  const autoDetectMutation = useMutation({
    mutationFn: () => autoDetectServerGeography(node.id),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['analytics-overview'] });
      onClose();
    },
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const parsedLatitude = latitude.trim() === '' ? undefined : Number(latitude);
    const parsedLongitude = longitude.trim() === '' ? undefined : Number(longitude);
    if ((parsedLatitude === undefined) !== (parsedLongitude === undefined)) {
      return;
    }
    if (parsedLatitude !== undefined && (!Number.isFinite(parsedLatitude) || parsedLatitude < -90 || parsedLatitude > 90)) {
      return;
    }
    if (parsedLongitude !== undefined && (!Number.isFinite(parsedLongitude) || parsedLongitude < -180 || parsedLongitude > 180)) {
      return;
    }
    mutation.mutate({
      country: country.trim(),
      region: region.trim(),
      city: city.trim(),
      latitude: parsedLatitude,
      longitude: parsedLongitude,
      source: 'manual',
    });
  };

  return (
    <form className="analytics-location-editor" onSubmit={submit}>
      <div className="analytics-location-editor-heading">
        <div>
          <strong>{t('analytics.configureLocationTitle')}</strong>
          <p>{t('analytics.configureLocationSubtitle')}</p>
        </div>
      </div>
      <div className="analytics-location-fields">
        <label>
          <span>{t('analytics.country')}</span>
          <input maxLength={128} onChange={(event) => setCountry(event.target.value)} value={country} />
        </label>
        <label>
          <span>{t('analytics.region')}</span>
          <input maxLength={128} onChange={(event) => setRegion(event.target.value)} value={region} />
        </label>
        <label>
          <span>{t('analytics.city')}</span>
          <input maxLength={128} onChange={(event) => setCity(event.target.value)} value={city} />
        </label>
        <label>
          <span>{t('analytics.latitude')}</span>
          <input inputMode="decimal" onChange={(event) => setLatitude(event.target.value)} placeholder="50.1109" value={latitude} />
        </label>
        <label>
          <span>{t('analytics.longitude')}</span>
          <input inputMode="decimal" onChange={(event) => setLongitude(event.target.value)} placeholder="8.6821" value={longitude} />
        </label>
      </div>
      {node.publicIp && (
        <div className="form-message">
          {t('analytics.autoLocationHint', { publicIp: node.publicIp })}
        </div>
      )}
      {mutation.isError && <div className="form-message form-message-error">{t('analytics.locationError')}</div>}
      {autoDetectMutation.isError && <div className="form-message form-message-error">{t('analytics.autoLocationError')}</div>}
      <div className="analytics-location-actions">
        <button className="button button-secondary" disabled={mutation.isPending || autoDetectMutation.isPending} onClick={onClose} type="button">{t('analytics.cancel')}</button>
        <button
          className="button button-secondary"
          disabled={!node.publicIp || mutation.isPending || autoDetectMutation.isPending}
          onClick={() => autoDetectMutation.mutate()}
          type="button"
        >
          {autoDetectMutation.isPending ? t('analytics.detectingLocation') : t('analytics.detectAutomatically')}
        </button>
        <button className="button button-primary" disabled={mutation.isPending || autoDetectMutation.isPending} type="submit">
          {mutation.isPending ? t('analytics.savingLocation') : t('analytics.saveLocation')}
        </button>
      </div>
    </form>
  );
}

function SelectedNodePanel({ node }: { node: AnalyticsNode }) {
  const navigate = useNavigate();
  const [editingLocation, setEditingLocation] = useState(false);
  // One Agent operation per node can be pending/running. Poll its *real*
  // lifecycle instead of leaving "queued" banners next to failed retries.
  const [requestedRun, setRequestedRun] = useState<DiagnosticRun | null>(null);
  const diagnosticMutation = useMutation({
    mutationFn: (profileKey: 'host_overview' | 'manager_certificate') =>
      profileKey === 'manager_certificate'
        ? runManagerCertificateDiagnostic(node.id)
        : runHostDiagnostic(node.id),
    onSuccess: run => setRequestedRun(run),
  });
  const resultQuery = useQuery({
    queryKey: ['analytics-diagnostic-run', node.id, requestedRun?.id],
    queryFn: () => getDiagnosticRun(node.id, requestedRun?.id ?? ''),
    enabled: Boolean(requestedRun && requestedRun.serverId === node.id),
    retry: 2,
    refetchInterval: query => {
      if (query.state.status === 'error') return false;
      return isDiagnosticActive(query.state.data?.status ?? requestedRun?.status) ? 2500 : false;
    },
  });
  const run = requestedRun?.serverId === node.id ? resultQuery.data ?? requestedRun : null;
  const diagnosticBusy = diagnosticMutation.isPending || Boolean(run && isDiagnosticActive(run.status) && !resultQuery.isError);
  const startDiagnostic = (profileKey: 'host_overview' | 'manager_certificate') => {
    if (diagnosticBusy) return;
    diagnosticMutation.reset();
    setRequestedRun(null);
    diagnosticMutation.mutate(profileKey);
  };

  useEffect(() => {
    setEditingLocation(false);
    setRequestedRun(null);
    diagnosticMutation.reset();
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [node.id]);

  if (editingLocation) {
    return <LocationEditor node={node} onClose={() => setEditingLocation(false)} />;
  }

  return (
    <aside className="analytics-node-inspector">
      <div className="analytics-node-inspector-header">
        <div>
          <span className="analytics-eyebrow">{t('analytics.node')}</span>
          <h2>{node.name}</h2>
          <p>{formatLocation(node)}</p>
        </div>
        <HealthPill state={node.health.state} />
      </div>

      <div className="analytics-node-facts">
        <div><span>{t('analytics.agent')}</span><strong>{agentStatusLabel(node.agent.status)}</strong></div>
        <div><span>{t('analytics.vpnCore')}</span><strong>{node.vpnCore.type || '—'} · {vpnCoreServiceStateLabel(node.vpnCore.serviceState)}</strong></div>
        <div><span>{t('analytics.memory')}</span><strong>{percent(node.resources.memoryUsageRatio)}</strong></div>
        <div><span>{t('analytics.disk')}</span><strong>{percent(node.resources.rootFsUsageRatio)}</strong></div>
        <div><span>{t('analytics.load')}</span><strong>{numberValue(node.resources.load1)}</strong></div>
        <div>
          <span>{node.agent.observationFresh ? t('analytics.observationFresh') : t('analytics.observationStale')}</span>
          <strong>{telemetryAgeLabel(node.agent.observationAgeSeconds)}</strong>
        </div>
      </div>

      {node.health.summary && (
        <div className={`analytics-next-action analytics-next-action--${node.health.state}`}>
          <span>{healthSummaryLabel(node.health.reasonCode, node.health.summary)}</span>
          {node.health.recommendedAction && (
            <div>
              <small>{t('analytics.recommendedAction')}</small>
              <strong>{actionLabel(node.health.recommendedAction)}</strong>
            </div>
          )}
        </div>
      )}

      {diagnosticMutation.isError && (
        <div className="form-message form-message-error" role="alert">
          {t(diagnosticRequestErrorKey(diagnosticMutation.error))}
        </div>
      )}
      {requestedRun && resultQuery.isError && (
        <div className="form-message form-message-error" role="alert">{t('analytics.diagnosticStatusUnavailable')}</div>
      )}
      {run && !resultQuery.isError && (
        <div
          className={`form-message ${run.status === 'failed' || run.state === 'unhealthy'
            ? 'form-message-error' : run.state === 'degraded' || run.state === 'unknown'
              ? 'form-message-warning' : 'form-message-success'}`}
          role="status"
        >
          {run.status === 'queued'
            ? t(run.profileKey === 'manager_certificate' ? 'analytics.certificateDiagnosticQueued' : 'analytics.diagnosticQueued')
            : run.status === 'running'
              ? t('analytics.diagnosticRunning')
              : run.status === 'failed'
                ? t('analytics.diagnosticExecutionFailed')
                : `${t('analytics.diagnosticCompleted')}: ${healthSummaryLabel(run.reasonCode, run.summary)}`}
        </div>
      )}

      <div className="analytics-node-actions">
        <button className="button button-primary" onClick={() => navigate(`/servers/${node.id}`)} type="button">
          {t('analytics.openServer')}
        </button>
        <button className="button button-secondary" disabled={diagnosticBusy} onClick={() => startDiagnostic('host_overview')} type="button">
          {t('analytics.runDiagnostics')}
        </button>
        <button className="button button-secondary" disabled={diagnosticBusy} onClick={() => startDiagnostic('manager_certificate')} type="button">
          {t('analytics.checkManagerCertificate')}
        </button>
        <button className="button button-secondary" onClick={() => setEditingLocation(true)} type="button">
          {t('analytics.configureLocation')}
        </button>
      </div>
    </aside>
  );
}

function AlertRow({ alert, onOpen }: { alert: AnalyticsAlert; onOpen: () => void }) {
  return (
    <button className="analytics-alert-row" onClick={onOpen} type="button">
      <span className={`analytics-alert-severity analytics-alert-severity--${alert.severity}`}>
        {alert.severity === 'critical' ? t('analytics.critical') : t('analytics.warning')}
      </span>
      <span className="analytics-alert-main">
        <strong>{alert.serverName}</strong>
        <span>{healthSummaryLabel(alert.reasonCode, alert.summary)}</span>
      </span>
      <span className="analytics-alert-meta">
        {alert.acknowledged && <small>{t('analytics.acknowledged')}</small>}
        <strong>{alert.state === 'firing' ? t('analytics.firing') : t('analytics.pending')}</strong>
        <time>{formatDateTime(alert.firingAt ?? alert.startedAt)}</time>
      </span>
    </button>
  );
}

export function AnalyticsPage() {
  const navigate = useNavigate();
  const overviewQuery = useQuery({
    queryKey: ['analytics-overview'],
    queryFn: getAnalyticsOverview,
    refetchInterval: 10_000,
    staleTime: 5_000,
  });

  const connectionsQuery = useQuery({
    queryKey: ['client-connections', 'analytics'],
    queryFn: () => getClientConnections(500),
    refetchInterval: 10_000,
  });
  const [selectedNodeId, setSelectedNodeId] = useState<string>();
  const [mapInspectorCollapsed, setMapInspectorCollapsed] = useState(false);

  const nodes = overviewQuery.data?.nodes ?? [];
  const selectedNode = useMemo(() => nodes.find((node) => node.id === selectedNodeId), [nodes, selectedNodeId]);

  useEffect(() => {
    if (nodes.length === 0) {
      setSelectedNodeId(undefined);
      return;
    }
    if (selectedNodeId && nodes.some((node) => node.id === selectedNodeId)) {
      return;
    }
    const firstProblem = [...nodes].sort((left, right) => HEALTH_RANK[right.health.state] - HEALTH_RANK[left.health.state])[0];
    setSelectedNodeId(firstProblem.id);
  }, [nodes, selectedNodeId]);

  const summary = overviewQuery.data?.summary;
  const locatedNodes = nodes.filter((node) => node.location.latitude !== undefined && node.location.longitude !== undefined);
  const problemNodes = nodes.filter((node) => node.health.state === 'unhealthy' || node.health.state === 'degraded');
  const inspectorLatchLabel = mapInspectorCollapsed ? t('analytics.mapShowInspector') : t('analytics.mapHideInspector');

  return (
    <section className="page analytics-page">
      <div className="page-header analytics-page-header">
        <div>
          <div className="analytics-live-label"><i aria-hidden="true" />{t('analytics.live')}</div>
          <h1>{t('analytics.title')}</h1>
          <p>{t('analytics.subtitle')}</p>
        </div>
        <span className="analytics-updated">
          {t('analytics.updated', { time: formatUpdatedTime(overviewQuery.data?.generatedAt) })}
        </span>
      </div>

      {overviewQuery.isLoading && <p className="empty-state">{t('analytics.loading')}</p>}
      {overviewQuery.isError && <div className="form-message form-message-error">{t('analytics.loadError')}</div>}

      {summary && (
        <>
          <div className="analytics-summary-grid">
            <SummaryCard label={t('analytics.totalNodes')} value={summary.totalNodes} />
            <SummaryCard label={t('analytics.healthy')} state="healthy" value={summary.healthyNodes} />
            <SummaryCard label={t('analytics.degraded')} state="degraded" value={summary.degradedNodes} />
            <SummaryCard label={t('analytics.unhealthy')} state="unhealthy" value={summary.unhealthyNodes} />
            <SummaryCard label={t('analytics.unknown')} state="unknown" value={summary.unknownNodes} />
            <SummaryCard label={t('analytics.activeAlerts')} value={summary.activeAlerts} />
            <SummaryCard label={t('analytics.criticalAlerts')} state="critical" value={summary.criticalAlerts} />
          </div>

          <section className="analytics-map-section panel">
            <div className="analytics-section-heading">
              <div>
                <h2>{t('analytics.worldMap')}</h2>
                <p>{t('analytics.worldMapSubtitle')}</p>
              </div>
              <span>{t('analytics.nodesLocated', { located: summary.locatedNodes, total: summary.totalNodes })}</span>
            </div>

            <div className={`analytics-map-layout${mapInspectorCollapsed ? ' is-inspector-collapsed' : ''}`}>
              <div className="analytics-map-stage">
                <AnalyticsWorldMap nodes={nodes} onSelectNode={setSelectedNodeId} selectedNodeId={selectedNodeId} />
                <button
                  className="analytics-map-inspector-latch"
                  type="button"
                  aria-expanded={!mapInspectorCollapsed}
                  aria-label={inspectorLatchLabel}
                  title={inspectorLatchLabel}
                  onClick={() => setMapInspectorCollapsed((value) => !value)}
                >
                  <MapInspectorLatchIcon collapsed={mapInspectorCollapsed} />
                </button>
                {locatedNodes.length === 0 && (
                  <div className="analytics-map-empty">
                    <strong>{t('analytics.mapNoLocations')}</strong>
                    <span>{t('analytics.mapNoLocationsHint')}</span>
                  </div>
                )}
                <small className="analytics-map-footnote">{t('analytics.coordinatesManaged')}</small>
              </div>
              {!mapInspectorCollapsed && (
                selectedNode
                  ? <SelectedNodePanel node={selectedNode} />
                  : <div className="analytics-map-selection-empty">{t('analytics.selectNode')}</div>
              )}
            </div>
          </section>

          <section className="analytics-attention-section">
            <div className="analytics-section-heading">
              <div>
                <h2>{t('analytics.attentionTitle')}</h2>
              </div>
            </div>
            {problemNodes.length === 0 ? (
              <div className="analytics-clear-state"><i aria-hidden="true" />{t('analytics.attentionClear')}</div>
            ) : (
              <div className="analytics-problem-grid">
                {problemNodes.slice(0, 6).map((node) => (
                  <button className="analytics-problem-card" key={node.id} onClick={() => setSelectedNodeId(node.id)} type="button">
                    <HealthPill state={node.health.state} />
                    <strong>{node.name}</strong>
                    <span>{healthSummaryLabel(node.health.reasonCode, node.health.summary || healthLabel(node.health.state))}</span>
                    {node.health.recommendedAction && <small>{actionLabel(node.health.recommendedAction)}</small>}
                  </button>
                ))}
              </div>
            )}
          </section>

          <section className="panel analytics-nodes-section">
            <div className="analytics-section-heading">
              <div>
                <h2>{t('analytics.nodeHealth')}</h2>
              </div>
            </div>
            <div className="analytics-node-table-wrap">
              <table className="analytics-node-table">
                <thead>
                  <tr>
                    <th>{t('analytics.node')}</th>
                    <th>{t('analytics.health')}</th>
                    <th>{t('analytics.location')}</th>
                    <th>{t('analytics.agent')}</th>
                    <th>{t('analytics.vpnCore')}</th>
                    <th>{t('analytics.memory')}</th>
                    <th>{t('analytics.disk')}</th>
                  </tr>
                </thead>
                <tbody>
                  {nodes.map((node) => (
                    <tr className={node.id === selectedNodeId ? 'is-selected' : undefined} key={node.id} onClick={() => setSelectedNodeId(node.id)}>
                      <td><button className="analytics-node-link" onClick={(event) => { event.stopPropagation(); navigate(`/servers/${node.id}`); }} type="button">{node.name}</button></td>
                      <td><HealthPill state={node.health.state} /></td>
                      <td>{formatLocation(node)}</td>
                      <td>{agentStatusLabel(node.agent.status)}</td>
                      <td>{node.vpnCore.type ? `${node.vpnCore.type} · ${vpnCoreServiceStateLabel(node.vpnCore.serviceState)}` : '—'}</td>
                      <td>{percent(node.resources.memoryUsageRatio)}</td>
                      <td>{percent(node.resources.rootFsUsageRatio)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>

          <section className="panel analytics-connections-section">
            <div className="analytics-section-heading analytics-connections-heading">
              <div>
                <h2>{t('analytics.connectionsTitle')}</h2>
                <p>{t('analytics.connectionsSubtitle')}</p>
              </div>
              <div className="analytics-connections-summary">
                <strong>{connectionsQuery.data?.summary.onlineUsers ?? 0}</strong>
                <span>{t('analytics.connectionsOnlineNow')}</span>
              </div>
            </div>
            {connectionsQuery.isError ? (
              <div className="empty-state">{t('analytics.connectionsError')}</div>
            ) : connectionsQuery.data?.items.length ? (
              <div className="analytics-node-table-wrap">
                <table className="analytics-node-table analytics-connections-table">
                  <thead>
                    <tr>
                      <th>{t('analytics.connectionsUser')}</th>
                      <th>{t('analytics.connectionsState')}</th>
                      <th>{t('analytics.connectionsServer')}</th>
                      <th>{t('analytics.connectionsAgent')}</th>
                      <th>{t('analytics.connectionsProtocol')}</th>
                      <th>{t('analytics.connectionsCount')}</th>
                      <th>{t('analytics.connectionsSeen')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {groupConnections(connectionsQuery.data.items).map((connection) => (
                      <tr key={connection.groupKey}>
                        <td><strong className="analytics-connection-user">{connection.accountName}</strong>{connection.email && <small>{connection.email}</small>}</td>
                        <td><ConnectionStatePill connection={connection} /></td>
                        <td>{connection.serverName}</td>
                        <td>{connection.agentName || '—'}</td>
                        <td>
                          <details>
                            <summary>{connection.details.map((item) => item.protocol).join(', ')}</summary>
                            {connection.details.map((item) => (
                              <div key={`${item.protocol}-${item.source}`}>
                                <span>{item.protocol} · </span>
                                <ConnectionStatePill connection={item} />
                                {item.state === 'online' && item.confidence === 'exact' && <span> · {item.connectionCount}</span>}
                              </div>
                            ))}
                          </details>
                        </td>
                        <td>{connection.state === 'online' ? connection.connectionCount : '—'}</td>
                        <td>{formatDateTime(connection.lastActivityAt ?? connection.observedAt)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <div className="empty-state analytics-connections-empty">{t('analytics.connectionsEmpty')}</div>
            )}
          </section>

          <section className="panel analytics-alerts-section">
            <div className="analytics-section-heading">
              <div>
                <h2>{t('analytics.alertsTitle')}</h2>
                <p>{t('analytics.alertsSubtitle')}</p>
              </div>
            </div>
            {overviewQuery.data?.alerts.length ? (
              <div className="analytics-alert-list">
                {overviewQuery.data.alerts.map((alert) => (
                  <AlertRow alert={alert} key={alert.id} onOpen={() => setSelectedNodeId(alert.serverId)} />
                ))}
              </div>
            ) : (
              <div className="empty-state">{t('analytics.noAlerts')}</div>
            )}
          </section>
        </>
      )}
    </section>
  );
}
