import { groupConnections } from '../../entities/connection/model/groupConnections';
import type { CSSProperties, ReactNode } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { getMe } from '../../entities/auth/api/authApi';
import { getAgents } from '../../entities/agent/api/agentApi';
import { getClientConnections, type ClientConnection } from '../../entities/connection/api/connectionApi';
import {
  getDashboardActivity,
  getDashboardNodes,
  getDashboardTraffic,
  type DashboardDailyTraffic,
  type DashboardNodeDistribution,
  type DashboardRecentAuditEvent,
  type DashboardRecentDeployment,
  type DashboardServerLoad,
} from '../../entities/dashboard/api/dashboardApi';
import { getServers } from '../../entities/server/api/serverApi';
import { getManagerHealth } from '../../entities/health/api/healthApi';
import { getPagedVpnAccounts } from '../../entities/vpnAccount/api/vpnAccountManagementApi';
import { t, translateStatus } from '../../shared/i18n/i18n';
import { StatusBadge } from '../../shared/ui/StatusBadge';
import { AnimatedValue } from './AnimatedValue';
import { GettingStartedWidget } from './GettingStartedWidget';
import { LiveStatusBar } from './LiveStatusBar';
import { resolveTrafficWidgetStatus, type TrafficWidgetStatus } from './trafficOverviewState';
import './DashboardPage.css';

function formatDate(value?: string | null): string {
  if (!value) {
    return t('common.notAvailable');
  }

  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function formatTrafficDate(value: string): string {
  const date = new Date(`${value}T00:00:00Z`);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleDateString(undefined, { day: 'numeric', month: 'short', timeZone: 'UTC' });
}

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) {
    return t('common.notAvailable');
  }
  if (bytes === 0) {
    return '0 B';
  }

  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  const unitIndex = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const value = bytes / (1024 ** unitIndex);
  const maximumFractionDigits = unitIndex === 0 || value >= 100 ? 0 : value >= 10 ? 1 : 2;

  return `${value.toLocaleString(undefined, { maximumFractionDigits })} ${units[unitIndex]}`;
}

function formatServerLoad(load?: DashboardServerLoad): string {
  if (load?.load1 === undefined || load.logicalCpus === undefined) {
    return t('common.notAvailable');
  }
  return `${load.load1.toFixed(2)} / ${load.logicalCpus} CPU`;
}

function getRegionCountryCode(region: string): string | null {
  const country = region.trim().split(',').at(-1)?.trim() ?? '';
  if (/^[A-Za-z]{2}$/.test(country)) return country.toUpperCase();

  const countryNames: Record<string, string> = {
    finland: 'FI',
    russia: 'RU',
    'russian federation': 'RU',
    'united states': 'US',
    'united states of america': 'US',
  };
  return countryNames[country.toLowerCase()] ?? null;
}


function getCountryMapRegion(countryCode: string): string | null {
  const code = countryCode.toUpperCase();
  if (['US', 'CA', 'MX'].includes(code)) return 'na';
  if (['GB', 'IE', 'IS', 'NO', 'SE', 'FI', 'DK', 'DE', 'NL', 'BE', 'FR', 'ES', 'PT', 'IT', 'CH', 'AT', 'PL', 'CZ', 'SK', 'HU', 'RO', 'BG', 'GR', 'EE', 'LV', 'LT', 'UA'].includes(code)) return 'eu';
  if (code === 'RU') return 'eurasia';
  if (['CN', 'JP', 'KR', 'KP', 'IN', 'SG', 'TH', 'VN', 'MY', 'ID', 'PH', 'KZ', 'UZ', 'AE', 'IL', 'TR'].includes(code)) return 'asia';
  if (['BR', 'AR', 'CL', 'PE', 'CO', 'UY', 'PY', 'BO', 'EC', 'VE'].includes(code)) return 'sa';
  if (['ZA', 'EG', 'MA', 'DZ', 'TN', 'NG', 'KE', 'ET', 'GH'].includes(code)) return 'af';
  if (['AU', 'NZ'].includes(code)) return 'oc';
  return null;
}

function getActorInitial(actor: string): string {
  const normalized = actor.trim();
  return normalized === '' ? '?' : normalized.slice(0, 1).toUpperCase();
}

function formatAuditResource(event: DashboardRecentAuditEvent): string {
  return event.resourceId ? `${event.resourceType} · ${event.resourceId}` : event.resourceType;
}

function KpiWidget({ title, value, meta, tone, icon, to, live }: { title: string; value: string; meta: string; tone: string; icon: string; to: string; live: boolean }) {
  return (
    <Link to={to} className={`dashboard-widget kpi-widget kpi-widget-${tone}${live ? ' kpi-widget-live' : ''}`}>
      <div>
        <div className="kpi-title"><span className="kpi-dot" />{title}</div>
        <div className="kpi-value"><AnimatedValue value={value} /></div>
        <div className="kpi-meta">{meta}</div>
      </div>
      <div className="kpi-icon" aria-hidden="true">{icon}</div>
    </Link>
  );
}

function WidgetPanel({ title, subtitle, children, className = '', action }: { title: string; subtitle?: string; children: ReactNode; className?: string; action?: ReactNode }) {
  return (
    <section className={`dashboard-widget ${className}`}>
      <div className="dashboard-widget-header">
        <div>
          <h2>{title}</h2>
          {subtitle && <span>{subtitle}</span>}
        </div>
        {action}
      </div>
      {children}
    </section>
  );
}

function UnavailableWidget({ title, subtitle, className }: { title: string; subtitle?: string; className: string }) {
  return (
    <WidgetPanel title={title} subtitle={subtitle} className={className}>
      <p className="empty-state">{t('common.notAvailable')}</p>
    </WidgetPanel>
  );
}

const HEARTBEAT_PATH = 'M0 24 H70 L78 24 L84 14 L90 34 L97 6 L104 40 L110 24 L118 24 H160 L166 18 L172 28 L178 24 H240';

function InfrastructureHealthWidget({
  managerHealthy,
  serversCount,
  agentsCount,
  serversAvailable,
  agentsAvailable,
}: {
  managerHealthy: boolean;
  serversCount: number;
  agentsCount: number;
  serversAvailable: boolean;
  agentsAvailable: boolean;
}) {
  const rows = [
    [t('dashboard.managerApi'), managerHealthy ? 'healthy' : 'warning', managerHealthy ? t('dashboard.managerOnline') : t('common.notAvailable')],
    [t('dashboard.servers'), serversAvailable ? (serversCount > 0 ? 'healthy' : 'pending') : 'warning', serversAvailable ? String(serversCount) : t('common.notAvailable')],
    [t('dashboard.agents'), agentsAvailable ? (agentsCount > 0 ? 'healthy' : 'pending') : 'warning', agentsAvailable ? String(agentsCount) : t('common.notAvailable')],
  ];

  const pulseState = rows.some(([, status]) => status === 'warning')
    ? 'warning'
    : rows.every(([, status]) => status === 'healthy') ? 'healthy' : 'pending';

  return (
    <WidgetPanel title={t('dashboard.infrastructureHealth')} className="health-widget">
      <div className="health-list">
        {rows.map(([label, status, value]) => (
          <div className="health-row" key={label}>
            <span>{label}</span>
            <strong>{value}</strong>
            <StatusBadge status={String(status)} />
          </div>
        ))}
      </div>
      <div className={`health-pulse health-pulse--${pulseState}`} aria-hidden="true">
        <svg viewBox="0 0 240 48" preserveAspectRatio="none">
          <path className="health-pulse-grid" d="M0 24 H240" />
          <path className="health-pulse-trace" d={HEARTBEAT_PATH} />
          <path className="health-pulse-line" pathLength={100} d={HEARTBEAT_PATH} />
        </svg>
      </div>
    </WidgetPanel>
  );
}

function NodeDistributionWidget({ distribution, available }: { distribution?: DashboardNodeDistribution; available: boolean }) {
  const locations = distribution?.locations ?? [];
  const totalServers = distribution?.totalServers ?? 0;
  const markers = locations.flatMap((item, index) => {
    const countryCode = getRegionCountryCode(item.location);
    const mapRegion = countryCode ? getCountryMapRegion(countryCode) : null;
    if (!countryCode || !mapRegion) {
      return [];
    }
    return [{
      key: `${item.location}-${index}`,
      countryCode,
      mapRegion,
      label: item.location,
      count: item.count,
    }];
  });

  return (
    <WidgetPanel title={t('dashboard.nodeDistribution')} className="node-widget">
      {!available ? (
        <p className="empty-state">{t('common.notAvailable')}</p>
      ) : totalServers === 0 ? (
        <p className="empty-state">{t('servers.emptyTitle')}</p>
      ) : (
        <div className="node-distribution-content">
          <div className="node-distribution-map" aria-label={t('dashboard.nodeDistribution')}>
            {markers.map((marker, index) => (
              <span
                className={`node-country-marker node-country-marker-${marker.mapRegion}`}
                key={marker.key}
                style={{ '--marker-index': index } as CSSProperties}
                title={`${marker.label} · ${marker.count}`}
              >
                <i className="node-country-marker-ping" aria-hidden="true" />
                <span className={`server-country-flag server-country-${marker.countryCode.toLowerCase()}`} aria-hidden="true" />
                <strong>{marker.count}</strong>
              </span>
            ))}
          </div>
          <div className="node-distribution-list">
            {locations.map((item, index) => {
              const label = item.location || t('common.notAvailable');
              const countryCode = getRegionCountryCode(item.location);
              const percentage = totalServers > 0 ? (item.count / totalServers) * 100 : 0;
              return (
                <div className="node-distribution-row" key={`${item.location}-${index}`}>
                  <div className="node-distribution-label">
                    <span className="node-distribution-location" title={label}>
                      {countryCode && <span className={`server-country-flag server-country-${countryCode.toLowerCase()}`} aria-hidden="true" />}
                      <span>{label}</span>
                    </span>
                    <strong>{item.count}</strong>
                  </div>
                  <div className="node-distribution-track"><span style={{ width: `${percentage}%` }} /></div>
                </div>
              );
            })}
          </div>
          <div className="node-distribution-total">{t('dashboard.nodes')}: <strong>{totalServers}</strong></div>
        </div>
      )}
    </WidgetPanel>
  );
}

function QuickActionsWidget() {
  const actions = [
    { label: t('dashboard.addServer'), to: '/servers' },
    { label: t('dashboard.createRoutingProfile'), to: '/routing-profiles' },
    { label: t('dashboard.selectDeploymentServer'), to: '/servers' },
    { label: t('navigation.vpnAccounts'), to: '/vpn-accounts' },
  ];

  return (
    <WidgetPanel title={t('dashboard.quickActions')} className="quick-actions-widget">
      <Link className="quick-primary-action" to="/vpn-accounts?create=1"><span>＋</span> {t('dashboard.createVpnAccount')}</Link>
      <div className="quick-action-list">
        {actions.map((action) => <Link to={action.to} key={action.label}><span>{action.label}</span><strong>›</strong></Link>)}
      </div>
    </WidgetPanel>
  );
}

function TrafficOverviewWidget({
  daily,
  status,
  onRetry,
}: {
  daily: DashboardDailyTraffic[];
  status: TrafficWidgetStatus;
  onRetry: () => void;
}) {
  const maximum = daily.reduce((value, item) => Math.max(value, item.totalBytes), 0);
  const total = daily.reduce((value, item) => value + item.totalBytes, 0);

  return (
    <WidgetPanel title={t('dashboard.trafficOverview')} subtitle={`(${t('dashboard.last30Days')})`} className="traffic-widget">
      {status !== 'ready' ? (
        <div className={`traffic-empty-state traffic-empty-state--${status}`} data-traffic-state={status}>
          <strong>
            {status === 'loading'
              ? t('dashboard.trafficLoading')
              : status === 'error'
                ? t('dashboard.trafficLoadFailed')
                : t('dashboard.trafficNoMeasurements')}
          </strong>
          {status !== 'loading' && (
            <>
              <p>{t(status === 'error' ? 'dashboard.trafficApiHelp' : 'dashboard.trafficCollectionHelp')}</p>
              {status === 'error' ? (
                <button className="small-button" type="button" onClick={onRetry}>
                  {t('dashboard.trafficRetry')}
                </button>
              ) : (
                <Link className="small-button" to="/servers">
                  {t('dashboard.trafficCheckServers')}
                </Link>
              )}
            </>
          )}
        </div>
      ) : (
        <div className="traffic-overview-content" data-traffic-state="ready">
          <div className="traffic-overview-total">
            <strong>{formatBytes(total)}</strong>
            <span>{t('dashboard.total')}</span>
          </div>
          <div className="traffic-overview-bars">
            {daily.map((item, index) => {
              const percentage = maximum > 0 ? (item.totalBytes / maximum) * 100 : 0;
              const showLabel = index === 0 || index === daily.length - 1 || index % 7 === 0;
              return (
                <div className="traffic-overview-column" key={item.date} style={{ '--bar-index': index } as CSSProperties} title={`${formatTrafficDate(item.date)} · ${formatBytes(item.totalBytes)}`}>
                  <div className="traffic-overview-bar-track">
                    <span className="traffic-overview-bar" style={{ height: `${percentage}%` }} />
                  </div>
                  <small>{showLabel ? formatTrafficDate(item.date) : ''}</small>
                </div>
              );
            })}
          </div>
        </div>
      )}
    </WidgetPanel>
  );
}

function ServersSummaryWidget({ servers, pending, available }: { pending: boolean; available: boolean; servers: Array<{ id: string; name: string; region: string; online: boolean; load: string; traffic: string; status: string }> }) {
  return (
    <WidgetPanel title={t('servers.title')} className="servers-summary-widget dashboard-table-widget">
      {!available ? (
        <p className="empty-state" role="status">{t(pending ? 'common.loading' : 'common.notAvailable')}</p>
      ) : servers.length === 0 ? (
        <p className="empty-state">{t('servers.emptyTitle')}</p>
      ) : (
        <div className="dashboard-table servers-summary-table">
          <div className="dashboard-table-row dashboard-table-head">
            <span>{t('servers.name')}</span><span>{t('servers.region')}</span><span>{t('dashboard.online')}</span><span>{t('servers.load')}</span><span>{t('servers.traffic24h')}</span><span>{t('servers.status')}</span>
          </div>
          {servers.map((server) => {
            const countryCode = getRegionCountryCode(server.region);

            return (
              <div className="dashboard-table-row" key={server.id}>
                <Link className="dashboard-entity-link" to={`/servers/${encodeURIComponent(server.id)}`} title={server.name}>{server.name}</Link>
                <span className="server-region-cell">
                  {countryCode && (
                    <span className={`server-country-flag server-country-${countryCode.toLowerCase()}`} aria-label={t('dashboard.countryCode', { code: countryCode })} />
                  )}
                  <span className="server-region-name">{server.region}</span>
                </span>
                <span className={server.online ? 'server-online-dot' : 'server-offline-dot'} />
                <span>{server.load}</span>
                <span>{server.traffic}</span>
                <StatusBadge status={server.status} />
              </div>
            );
          })}
        </div>
      )}
      <Link className="widget-link" to="/servers">{t('dashboard.allServers')} →</Link>
    </WidgetPanel>
  );
}

function OnlineUsersWidget({ items, available }: { items: ClientConnection[]; available: boolean }) {
  const visibleItems = groupConnections(items)
    .filter((item) => (item.state === 'online' && item.confidence === 'exact') || item.state === 'recently_active');
  return (
    <WidgetPanel
      title={t('dashboard.onlineVpnUsers')}
      subtitle={t('dashboard.onlineVpnUsersSubtitle')}
      className="online-users-widget dashboard-table-widget"
      action={<Link className="widget-link online-users-all-link" to="/analytics">{t('dashboard.allConnections')} →</Link>}
    >
      {!available ? (
        <p className="empty-state">{t('common.notAvailable')}</p>
      ) : visibleItems.length === 0 ? (
        <p className="empty-state">{t('dashboard.noOnlineVpnUsers')}</p>
      ) : (
        <div className="dashboard-table online-users-table" tabIndex={0} role="region" aria-label={t('dashboard.onlineVpnUsers')}>
          <div className="dashboard-table-row dashboard-table-head">
            <span>{t('dashboard.user')}</span><span>{t('dashboard.connectionState')}</span><span>{t('dashboard.server')}</span><span>{t('dashboard.agentNode')}</span><span>{t('dashboard.connections')}</span>
          </div>
          {visibleItems.map((item) => {
            const online = item.state === 'online' && item.confidence === 'exact';
            return (
              <div className="dashboard-table-row" key={item.groupKey}>
                <Link className="dashboard-entity-link" to={`/vpn-accounts/${encodeURIComponent(item.vpnAccountId)}/access`} title={item.email || item.accountName}>{item.accountName}</Link>
                <span className={`online-user-state online-user-state--${online ? 'online' : 'recent'}`}>
                  <i aria-hidden="true" />
                  {t(online ? 'dashboard.online' : 'dashboard.recentlyActive')}
                </span>
                <span title={item.serverName}>{item.serverName}</span>
                <span title={item.agentName}>{item.agentName || '—'}</span>
                <span className="online-user-count">{item.state === 'online' ? item.connectionCount : '—'}</span>
              </div>
            );
          })}
        </div>
      )}
    </WidgetPanel>
  );
}

function RecentDeploymentsWidget({ deployments, available }: { deployments: DashboardRecentDeployment[]; available: boolean }) {
  return (
    <WidgetPanel title={t('dashboard.recentDeployments')} className="deployments-widget dashboard-table-widget">
      {!available ? (
        <p className="empty-state">{t('common.notAvailable')}</p>
      ) : deployments.length === 0 ? (
        <p className="empty-state">0</p>
      ) : (
        <div className="dashboard-table deployments-table">
          <div className="dashboard-table-row dashboard-table-head">
            <span>{t('dashboard.configurationColumn')}</span>
            <span>{t('dashboard.target')}</span>
            <span>{t('agents.action')}</span>
            <span>{t('servers.status')}</span>
            <span>{t('dashboard.time')}</span>
          </div>
          {deployments.map((deployment) => (
            <div className="dashboard-table-row" key={deployment.id}>
              <Link className="dashboard-entity-link" to={`/servers/${encodeURIComponent(deployment.serverId)}`} title={deployment.configVersionId}>v{deployment.configVersion}</Link>
              <span title={deployment.serverName}>{deployment.serverName}</span>
              <span>{deployment.action}</span>
              <StatusBadge status={deployment.status} />
              <span title={formatDate(deployment.completedAt ?? deployment.createdAt)}>{formatDate(deployment.completedAt ?? deployment.createdAt)}</span>
            </div>
          ))}
        </div>
      )}
    </WidgetPanel>
  );
}

function RecentAuditEventsWidget({ events, available }: { events: DashboardRecentAuditEvent[]; available: boolean }) {
  return (
    <WidgetPanel title={t('dashboard.recentAuditEvents')} className="audit-widget">
      {!available ? (
        <p className="empty-state">{t('common.notAvailable')}</p>
      ) : events.length === 0 ? (
        <p className="empty-state">0</p>
      ) : (
        <div className="audit-list">
          {events.map((event) => (
            <div className="audit-row" key={event.id}>
              <span className="audit-avatar" aria-hidden="true">{getActorInitial(event.actor)}</span>
              <div>
                <strong title={event.actor}>{event.actor}</strong>
                <p title={`${event.action} · ${formatAuditResource(event)}`}>{event.action} · {formatAuditResource(event)}</p>
              </div>
              <small title={formatDate(event.createdAt)}>{formatDate(event.createdAt)}</small>
              <span className="audit-area-badge">{translateStatus(event.result)}</span>
            </div>
          ))}
        </div>
      )}
    </WidgetPanel>
  );
}

export function DashboardPage() {
  const managerHealthQuery = useQuery({
    queryKey: ['manager-health'],
    queryFn: getManagerHealth,
    refetchInterval: 10_000,
  });

  const serversQuery = useQuery({
    queryKey: ['servers'],
    queryFn: getServers,
    refetchInterval: 10_000,
  });

  const agentsQuery = useQuery({
    queryKey: ['agents'],
    queryFn: getAgents,
    refetchInterval: 10_000,
  });

  const dashboardActivityQuery = useQuery({
    queryKey: ['dashboard', 'activity'],
    queryFn: getDashboardActivity,
    refetchInterval: 10_000,
  });

  const dashboardTrafficQuery = useQuery({
    queryKey: ['dashboard', 'traffic'],
    queryFn: getDashboardTraffic,
    refetchInterval: 60_000,
  });

  const dashboardNodesQuery = useQuery({
    queryKey: ['dashboard', 'nodes'],
    queryFn: getDashboardNodes,
    refetchInterval: 60_000,
  });

  const vpnAccountsCountQuery = useQuery({
    queryKey: ['vpn-accounts', 'dashboard-count', 'all'],
    queryFn: () => getPagedVpnAccounts({ page: 1, pageSize: 1 }),
    refetchInterval: 10_000,
  });

  const clientConnectionsQuery = useQuery({
    queryKey: ['client-connections', 'dashboard'],
    queryFn: () => getClientConnections(20),
    refetchInterval: 10_000,
  });

  const activeVpnAccountsCountQuery = useQuery({
    queryKey: ['vpn-accounts', 'dashboard-count', 'active'],
    queryFn: () => getPagedVpnAccounts({ status: 'active', page: 1, pageSize: 1 }),
    refetchInterval: 10_000,
  });

  useQuery({
    queryKey: ['me'],
    queryFn: getMe,
    retry: false,
  });

  const managerHealthy = managerHealthQuery.isSuccess;
  const servers = serversQuery.data?.items ?? [];
  const agents = agentsQuery.data?.items ?? [];

  const serversCount = servers.length;
  const activeServersCount = servers.filter((server) => server.status === 'active').length;
  const connectedServersCount = servers.filter((server) => server.agent?.status === 'online').length;
  const disconnectedServersCount = Math.max(serversCount - connectedServersCount, 0);

  const agentsCount = agents.length;
  const onlineAgentsCount = agents.filter((agent) => agent.status === 'online').length;
  const offlineAgentsCount = Math.max(agentsCount - onlineAgentsCount, 0);

  const activeVpnAccounts = activeVpnAccountsCountQuery.isSuccess ? activeVpnAccountsCountQuery.data.total : '—';
  const onlineVpnUsers = clientConnectionsQuery.data?.summary.onlineUsers ?? 0;
  const vpnAccountsCount = vpnAccountsCountQuery.isSuccess ? vpnAccountsCountQuery.data.total : '—';
  const trafficApiAvailable = dashboardTrafficQuery.isSuccess;
  const monthlyTrafficAvailable = trafficApiAvailable && dashboardTrafficQuery.data?.monthlyAvailable === true;
  const trafficWidgetStatus = resolveTrafficWidgetStatus(
    dashboardTrafficQuery.status,
    dashboardTrafficQuery.data?.dailyAvailable,
    dashboardTrafficQuery.data?.daily,
  );
  const monthlyTraffic = dashboardTrafficQuery.data?.monthly;
  const serverTrafficById = new Map((dashboardTrafficQuery.data?.servers ?? []).map((item) => [item.serverId, item]));
  const serverLoadById = new Map((dashboardNodesQuery.data?.serverLoads ?? []).map((item) => [item.serverId, item]));

  const displayServers = servers.slice(0, 5).map((server, index) => {
    const serverTraffic = serverTrafficById.get(server.id);
    return {
      id: server.id,
      name: server.name || `server-${index + 1}`,
      region: server.location || t('common.notAvailable'),
      online: server.agent?.status === 'online',
      load: dashboardNodesQuery.isSuccess ? formatServerLoad(serverLoadById.get(server.id)) : t('common.notAvailable'),
      traffic: trafficApiAvailable && serverTraffic?.available ? formatBytes(serverTraffic.totalBytes) : 'N/A',
      status: server.status || 'unknown',
    };
  });

  const liveUpdatedAt = Math.max(
    managerHealthQuery.dataUpdatedAt,
    serversQuery.dataUpdatedAt,
    agentsQuery.dataUpdatedAt,
    clientConnectionsQuery.dataUpdatedAt,
  );

  return (
    <section className="page dashboard-page dashboard-reference-page dashboard-fidelity-page">
      <LiveStatusBar
        updatedAt={liveUpdatedAt}
        serverTime={managerHealthQuery.data?.timestamp}
        serverTimeReceivedAt={managerHealthQuery.dataUpdatedAt}
      />
      <div className="dashboard-reference-grid">
        <GettingStartedWidget />
        <KpiWidget
          title={t('dashboard.activeServers')}
          to="/servers"
          value={serversQuery.isSuccess ? `${activeServersCount} / ${serversCount}` : '—'}
          meta={serversQuery.isSuccess ? `${t('dashboard.online')}: ${connectedServersCount} · ${t('dashboard.offline')}: ${disconnectedServersCount}` : t(serversQuery.isPending ? 'common.loading' : 'common.notAvailable')}
          tone="blue"
          icon="▤"
          live={serversQuery.isSuccess && activeServersCount > 0}
        />
        <KpiWidget
          title={t('dashboard.onlineAgents')}
          to="/servers"
          value={agentsQuery.isSuccess ? `${onlineAgentsCount} / ${agentsCount}` : '—'}
          meta={agentsQuery.isSuccess ? `${t('dashboard.connected')}: ${onlineAgentsCount} · ${t('dashboard.noConnection')}: ${offlineAgentsCount}` : t(agentsQuery.isPending ? 'common.loading' : 'common.notAvailable')}
          tone="cyan"
          icon="⌘"
          live={agentsQuery.isSuccess && onlineAgentsCount > 0}
        />
        <KpiWidget
          title={t('dashboard.onlineVpnUsers')}
          to="/analytics"
          value={clientConnectionsQuery.isSuccess ? String(onlineVpnUsers) : 'N/A'}
          meta={t('dashboard.activeAccountsMeta', { active: activeVpnAccounts, total: vpnAccountsCount })}
          tone="purple"
          icon="◉"
          live={clientConnectionsQuery.isSuccess && onlineVpnUsers > 0}
        />
        <KpiWidget
          title={t('dashboard.monthlyTraffic')}
          to="/analytics"
          value={monthlyTrafficAvailable && monthlyTraffic ? formatBytes(monthlyTraffic.totalBytes) : 'N/A'}
          meta={monthlyTrafficAvailable && monthlyTraffic
            ? `${t('dashboard.inboundTraffic')}: ${formatBytes(monthlyTraffic.rxBytes)} · ${t('dashboard.outboundTraffic')}: ${formatBytes(monthlyTraffic.txBytes)}`
            : t(dashboardTrafficQuery.isPending
              ? 'dashboard.trafficLoading'
              : dashboardTrafficQuery.isError ? 'dashboard.trafficLoadFailed' : 'dashboard.trafficNoMeasurements')}
          tone="amber"
          icon="☁"
          live={monthlyTrafficAvailable}
        />

        <InfrastructureHealthWidget
          managerHealthy={managerHealthy}
          serversCount={serversCount}
          agentsCount={agentsCount}
          serversAvailable={serversQuery.isSuccess}
          agentsAvailable={agentsQuery.isSuccess}
        />
        <NodeDistributionWidget distribution={dashboardNodesQuery.data} available={dashboardNodesQuery.isSuccess} />
        <TrafficOverviewWidget
          daily={dashboardTrafficQuery.data?.daily ?? []}
          status={trafficWidgetStatus}
          onRetry={() => void dashboardTrafficQuery.refetch()}
        />
        <QuickActionsWidget />
        <div className="dashboard-activity-grid">
          <ServersSummaryWidget servers={displayServers} pending={serversQuery.isPending} available={serversQuery.isSuccess} />
          <OnlineUsersWidget items={clientConnectionsQuery.data?.items ?? []} available={clientConnectionsQuery.isSuccess} />
          <RecentDeploymentsWidget
            deployments={dashboardActivityQuery.data?.recentDeployments ?? []}
            available={dashboardActivityQuery.isSuccess}
          />
        </div>
        <UnavailableWidget title={t('dashboard.trafficTypes')} subtitle={`(${t('dashboard.month')})`} className="traffic-types-widget" />
        <RecentAuditEventsWidget
          events={dashboardActivityQuery.data?.recentAuditEvents ?? []}
          available={dashboardActivityQuery.isSuccess}
        />
      </div>
      <div className="dashboard-server-time">{t('dashboard.serverTime', { time: formatDate(managerHealthQuery.data?.timestamp) })}</div>
    </section>
  );
}
