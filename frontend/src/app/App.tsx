import { useEffect, useRef, useState, type ReactNode } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, Navigate, NavLink, Route, Routes, useLocation, useNavigate } from 'react-router-dom';
import { DashboardPage } from '../pages/dashboard/DashboardPage';
import { AnalyticsPage } from '../pages/analytics/AnalyticsPage';
import { ServersPage } from '../pages/servers/ServersPage';
import { ServerDetailsPage } from '../pages/servers/ServerDetailsPage';
import { ProtocolSettingsPage } from '../pages/protocol-settings/ProtocolSettingsPage';
import { transferReservationsKey } from '../entities/vpnAccount/api/vpnAccountTransferApi';
import { TransferReservationNotice } from '../pages/vpn-accounts/TransferReservationNotice';
import { VpnAccountsPage } from '../pages/vpn-accounts/VpnAccountsPage';
import { RoutingProfilesPage } from '../pages/routing-profiles/RoutingProfilesPage';
import { NodeGroupsPage } from '../pages/node-groups/NodeGroupsPage';
import { LoginPage } from '../pages/login/LoginPage';
import { SetupPage } from '../pages/setup/SetupPage';
import { SecurityPage } from '../pages/settings/SecurityPage';
import { SettingsPage } from '../pages/settings/SettingsPage';
import { PortalPage } from '../pages/portal/PortalPage';
import { getMe, logout, type AuthUser } from '../entities/auth/api/authApi';
import { getManagerHealth } from '../entities/health/api/healthApi';
import { ApiError, clearAuthToken, getAuthToken } from '../shared/api/client';
import { t } from '../shared/i18n/i18n';
import { useLocale } from '../shared/i18n/useLocale';
import { LocaleSwitcher } from '../shared/ui/LocaleSwitcher';

const routeGateSymbolUrl = new URL('../shared/assets/routegate-symbol.svg', import.meta.url).href;
const ADMIN_SIDEBAR_STORAGE_KEY = 'routegate.admin.sidebarCollapsed';

function readStoredSidebarCollapsed(): boolean {
  if (typeof window === 'undefined') {
    return false;
  }
  return window.localStorage.getItem(ADMIN_SIDEBAR_STORAGE_KEY) === 'true';
}

function getTimestampOffsetMinutes(timestamp: string): number {
  const match = timestamp.match(/(Z|([+-])(\d{2}):(\d{2}))$/);
  if (!match || match[1] === 'Z') {
    return 0;
  }
  const sign = match[2] === '-' ? -1 : 1;
  return sign * ((Number(match[3]) * 60) + Number(match[4]));
}

function formatServerTime(timestamp?: string | null, elapsedMilliseconds = 0): string {
  if (!timestamp) {
    return t('common.notAvailable');
  }

  const parsed = new Date(timestamp);
  if (Number.isNaN(parsed.getTime())) {
    return timestamp;
  }

  const offsetMinutes = getTimestampOffsetMinutes(timestamp);
  const shifted = new Date(parsed.getTime() + elapsedMilliseconds + (offsetMinutes * 60_000));
  const pad = (value: number) => String(value).padStart(2, '0');
  const absoluteOffset = Math.abs(offsetMinutes);
  const offsetHours = Math.floor(absoluteOffset / 60);
  const offsetRemainder = absoluteOffset % 60;
  const offsetLabel = offsetMinutes === 0
    ? 'UTC'
    : `UTC${offsetMinutes > 0 ? '+' : '-'}${offsetHours}${offsetRemainder ? `:${pad(offsetRemainder)}` : ''}`;

  return `${pad(shifted.getUTCDate())}.${pad(shifted.getUTCMonth() + 1)}.${shifted.getUTCFullYear()} ${pad(shifted.getUTCHours())}:${pad(shifted.getUTCMinutes())}:${pad(shifted.getUTCSeconds())} (${offsetLabel})`;
}

type IconName =
  | 'overview'
  | 'analytics'
  | 'servers'
  | 'accounts'
  | 'deploy'
  | 'routing'
  | 'portal'
  | 'login'
  | 'security'
  | 'licensing'
  | 'appliance'
  | 'search'
  | 'settings'
  | 'menu'
  | 'collapse';

function Icon({ name }: { name: IconName }) {
  const paths: Record<IconName, ReactNode> = {
    overview: <><path d="M3.5 11.5 12 4l8.5 7.5" /><path d="M5.5 10.5V20h13v-9.5" /><path d="M9.5 20v-5.5h5V20" /></>,
    analytics: <><circle cx="12" cy="12" r="8.5" /><path d="M3.7 9h16.6M3.7 15h16.6M12 3.5c2.1 2.3 3.2 5.1 3.2 8.5S14.1 18.2 12 20.5M12 3.5C9.9 5.8 8.8 8.6 8.8 12s1.1 6.2 3.2 8.5" /></>,
    servers: <><rect x="4" y="5" width="16" height="5" rx="1.4" /><rect x="4" y="14" width="16" height="5" rx="1.4" /><path d="M7 7.5h.01M7 16.5h.01M11 7.5h6M11 16.5h6" /></>,
    accounts: <><circle cx="8.5" cy="10" r="3" /><circle cx="15.5" cy="8" r="2.5" /><path d="M3.5 20a5 5 0 0 1 10 0" /><path d="M13.5 14.5A4.5 4.5 0 0 1 20.5 18" /></>,
    deploy: <><path d="M6 5h12v14H6z" /><path d="M9 8h6M9 12h6M9 16h4" /></>,
    routing: <><circle cx="5" cy="12" r="2" /><circle cx="19" cy="6" r="2" /><circle cx="19" cy="18" r="2" /><path d="M7 12h4c3 0 4-6 6-6M7 12h4c3 0 4 6 6 6" /></>,
    portal: <><rect x="5" y="6" width="14" height="12" rx="2" /><path d="M9 10h6M9 14h4" /></>,
    login: <><path d="M10 17l5-5-5-5" /><path d="M15 12H3" /><path d="M13 4h5a3 3 0 0 1 3 3v10a3 3 0 0 1-3 3h-5" /></>,
    security: <><path d="M12 3 5 6v5c0 4.5 2.8 8.5 7 10 4.2-1.5 7-5.5 7-10V6l-7-3Z" /><path d="m9 12 2 2 4-5" /></>,
    licensing: <><circle cx="12" cy="12" r="7" /><path d="M9.5 12.5 11 14l3.5-4" /></>,
    appliance: <><rect x="4" y="6" width="16" height="12" rx="2" /><path d="M8 10h8M8 14h3M15 14h1" /></>,
    search: <><circle cx="10.5" cy="10.5" r="5.5" /><path d="m15 15 4 4" /></>,
    settings: <><circle cx="12" cy="12" r="3" /><path d="M12 3v3M12 18v3M3 12h3M18 12h3M5.6 5.6l2.1 2.1M16.3 16.3l2.1 2.1M18.4 5.6l-2.1 2.1M7.7 16.3l-2.1 2.1" /></>,
    menu: <><path d="M5 7h14M5 12h14M5 17h14" /></>,
    collapse: <><path d="M15 6 9 12l6 6" /><path d="M9 12h12" /></>,
  };

  return (
    <svg aria-hidden="true" className="rg-icon" viewBox="0 0 24 24">
      {paths[name]}
    </svg>
  );
}

function BrandMark() {
  return (
    <span className="brand-mark" aria-hidden="true">
      <img src={routeGateSymbolUrl} alt="" />
    </span>
  );
}

function PortalShell() {
  return (
    <div className="portal-app-shell">
      <header className="portal-topbar">
        <Link className="portal-brand" to="/portal">
          <BrandMark />
          <div>
            <div className="brand-title">RouteGate</div>
            <div className="brand-subtitle">{t('app.portalSubtitle')}</div>
          </div>
        </Link>

        <div className="portal-topbar-actions">
          <nav className="portal-topnav">
            <Link to="/portal">{t('navigation.userPortal')}</Link>
            <Link to="/">{t('navigation.adminUi')}</Link>
            <Link to="/login">{t('navigation.login')}</Link>
          </nav>
          <LocaleSwitcher />
        </div>
      </header>

      <main className="portal-main">
        <Routes>
          <Route path="/portal" element={<PortalPage />} />
          <Route path="/portal/profiles/:profileId" element={<PortalPage />} />
          <Route path="*" element={<Navigate to="/portal" replace />} />
        </Routes>
      </main>

      <footer className="admin-statusbar portal-statusbar">
        <span>{t('app.footerProduct')}</span>
        <span>{t('app.version')}</span>
        <strong><span className="status-dot status-dot-ok" /> {t('dashboard.systemsOperational')}</strong>
      </footer>
    </div>
  );
}

interface AuthShellProps {
  children?: ReactNode;
  onLogin?: () => void;
}

function AuthShell({ children, onLogin }: AuthShellProps) {
  return (
    <div className="auth-app-shell">
      <header className="auth-topbar">
        <Link className="portal-brand" to="/">
          <BrandMark />
          <div>
            <div className="brand-title">RouteGate</div>
            <div className="brand-subtitle">{t('app.adminSubtitle')}</div>
          </div>
        </Link>

        <div className="auth-topbar-actions">
          <nav className="portal-topnav">
            <Link to="/">{t('navigation.adminUi')}</Link>
            <Link to="/portal">{t('navigation.userPortal')}</Link>
          </nav>
          <LocaleSwitcher />
        </div>
      </header>

      <main className="auth-main">
        {children ?? (
          <Routes>
            <Route path="/login" element={<LoginPage onLogin={onLogin} />} />
            <Route path="/setup" element={<SetupPage onLogin={onLogin} />} />
            <Route path="*" element={<Navigate to="/login" replace />} />
          </Routes>
        )}
      </main>
    </div>
  );
}

interface AdminShellProps {
  isLoggingOut: boolean;
  onLogout: () => void;
  user?: AuthUser;
  sessionNotice?: ReactNode;
}

function getUserDisplayName(user?: AuthUser): string {
  return user?.displayName?.trim() || user?.email?.trim() || t('common.unknown');
}

function getUserInitials(user?: AuthUser): string {
  const displayName = getUserDisplayName(user);
  const nameParts = displayName.split(/\s+/).filter(Boolean);

  if (nameParts.length >= 2) {
    return `${nameParts[0][0]}${nameParts[1][0]}`.toUpperCase();
  }

  const emailName = user?.email?.split('@')[0]?.trim();
  const source = emailName || displayName;

  return source.slice(0, 2).toUpperCase();
}

function ProfileMenu({ isLoggingOut, onLogout, user }: AdminShellProps) {
  const [isOpen, setIsOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement | null>(null);
  const displayName = getUserDisplayName(user);

  useEffect(() => {
    if (!isOpen) {
      return;
    }

    const handlePointerDown = (event: PointerEvent) => {
      if (menuRef.current?.contains(event.target as Node)) {
        return;
      }

      setIsOpen(false);
    };

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setIsOpen(false);
      }
    };

    document.addEventListener('pointerdown', handlePointerDown);
    document.addEventListener('keydown', handleKeyDown);

    return () => {
      document.removeEventListener('pointerdown', handlePointerDown);
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [isOpen]);

  const handleLogout = () => {
    setIsOpen(false);
    onLogout();
  };

  return (
    <div className="admin-profile-menu" ref={menuRef}>
      <button
        aria-expanded={isOpen}
        aria-haspopup="menu"
        aria-label={t('topbar.profileMenu')}
        className="admin-profile-chip"
        onClick={() => setIsOpen((current) => !current)}
        type="button"
      >
        <span>{getUserInitials(user)}</span>
        <div>
          <strong>{displayName}</strong>
          <small>{t('topbar.adminRole')}</small>
        </div>
        <i aria-hidden="true" />
      </button>

      {isOpen && (
        <div className="admin-profile-dropdown" role="menu">
          <Link
            className="admin-profile-menu-item rg101-profile-link"
            onClick={() => setIsOpen(false)}
            role="menuitem"
            to="/settings/security"
          >
            {t('security.profileMenu')}
          </Link>
          <Link className="admin-profile-menu-item" role="menuitem" to="/portal" onClick={() => setIsOpen(false)}>
            {t('portalV2.openPortal')}
          </Link>
          <button
            className="admin-profile-menu-item"
            disabled={isLoggingOut}
            onClick={handleLogout}
            role="menuitem"
            type="button"
          >
            {isLoggingOut ? t('auth.loggingOut') : t('auth.logout')}
          </button>
        </div>
      )}
    </div>
  );
}

function AdminShell({ isLoggingOut, onLogout, user, sessionNotice }: AdminShellProps) {
  const [isSidebarCollapsed, setIsSidebarCollapsed] = useState(readStoredSidebarCollapsed);
  const [clockTick, setClockTick] = useState(() => Date.now());
  const managerHealthQuery = useQuery({
    queryKey: ['manager-health'],
    queryFn: getManagerHealth,
    refetchInterval: 10_000,
  });

  useEffect(() => {
    window.localStorage.setItem(ADMIN_SIDEBAR_STORAGE_KEY, String(isSidebarCollapsed));
  }, [isSidebarCollapsed]);

  useEffect(() => {
    const intervalId = window.setInterval(() => setClockTick(Date.now()), 1_000);
    return () => window.clearInterval(intervalId);
  }, []);

  const serverTimeElapsed = managerHealthQuery.dataUpdatedAt > 0
    ? Math.max(0, clockTick - managerHealthQuery.dataUpdatedAt)
    : 0;

  const adminNavigationItems = [
    { to: '/', label: t('navigation.overview'), icon: 'overview' as const, end: true },
    { to: '/analytics', label: t('navigation.analytics'), icon: 'analytics' as const },
    { to: '/servers', label: t('navigation.servers'), icon: 'servers' as const },
    { to: '/vpn-accounts', label: t('navigation.vpnAccounts'), icon: 'accounts' as const },
    { to: '/protocol-settings', label: t('navigation.configDeploy'), icon: 'deploy' as const },
    { to: '/routing-profiles', label: t('navigation.routingProfiles'), icon: 'routing' as const },
    { to: '/node-groups', label: t('navigation.nodeGroups'), icon: 'servers' as const },
  ];
  const secondaryNavigationItems = [
    { label: t('navigation.licensing'), icon: 'licensing' as const },
    { label: t('navigation.appliance'), icon: 'appliance' as const },
  ];

  return (
    <div className={`app-shell routegate-admin-shell routegate-reference-shell${isSidebarCollapsed ? ' sidebar-collapsed' : ''}`}>
      <aside className="sidebar routegate-sidebar">
        <div className="brand routegate-brand">
          <BrandMark />
          <div>
            <div className="brand-title">RouteGate</div>
            <div className="brand-subtitle">{t('app.adminSubtitle')}</div>
          </div>
        </div>

        <nav className="nav routegate-nav">
          {adminNavigationItems.map((item) => (
            <NavLink
              className={({ isActive }) => (isActive ? 'nav-link nav-link-active' : 'nav-link')}
              end={item.end}
              key={item.to}
              to={item.to}
            >
              <span className="nav-icon" aria-hidden="true"><Icon name={item.icon} /></span>
              <span>{item.label}</span>
            </NavLink>
          ))}
        </nav>

        <nav className="nav routegate-nav routegate-nav-secondary" aria-label={t('navigation.secondary')}>
          <NavLink
            className={({ isActive }) => (isActive ? 'nav-link nav-link-active' : 'nav-link')}
            to="/settings/security"
          >
            <span className="nav-icon" aria-hidden="true"><Icon name="security" /></span>
            <span>{t('navigation.security')}</span>
          </NavLink>
          <NavLink
            className={({ isActive }) => (isActive ? 'nav-link nav-link-active' : 'nav-link')}
            end
            to="/settings"
          >
            <span className="nav-icon" aria-hidden="true"><Icon name="settings" /></span>
            <span>{t('navigation.settings')}</span>
          </NavLink>
          {secondaryNavigationItems.map((item) => (
            <span className="nav-link nav-link-muted" key={item.label}>
              <span className="nav-icon" aria-hidden="true"><Icon name={item.icon} /></span>
              <span>{item.label}</span>
            </span>
          ))}
        </nav>

        <div className="sidebar-spacer" />

        <div className="sidebar-license-card">
          <div className="sidebar-license-header">
            <span>{t('dashboard.license')}</span>
            <strong>{t('dashboard.licenseActive')}</strong>
          </div>
          <p>{t('dashboard.licenseExpires')}</p>
          <p>{t('dashboard.nodesUsage')}</p>
          <div className="license-progress-track"><span /></div>
        </div>

        <button
          aria-expanded={!isSidebarCollapsed}
          className="sidebar-control sidebar-collapse-control"
          onClick={() => setIsSidebarCollapsed((current) => !current)}
          title={t('topbar.toggleSidebar')}
          type="button"
        >
          <Icon name="collapse" />
          <span>{t('navigation.collapse')}</span>
        </button>
      </aside>

      <div className="admin-workspace">
        <header className="admin-topbar">
          <button
            aria-label={t('topbar.toggleSidebar')}
            className="topbar-menu-button"
            onClick={() => setIsSidebarCollapsed((current) => !current)}
            type="button"
          ><Icon name="menu" /></button>
          <label className="topbar-search">
            <Icon name="search" />
            <input placeholder={t('topbar.searchPlaceholder')} />
            <kbd>{t('topbar.shortcut')}</kbd>
          </label>
          <div className="topbar-actions">
            <LocaleSwitcher />
            <ProfileMenu isLoggingOut={isLoggingOut} onLogout={onLogout} user={user} />
          </div>
        </header>

        <main className="main admin-main">
          {sessionNotice}
          <TransferReservationNotice />
          <Routes>
            <Route path="/" element={<DashboardPage />} />
            <Route path="/analytics" element={<AnalyticsPage />} />
            <Route path="/servers" element={<ServersPage />} />
            <Route path="/servers/:serverId" element={<ServerDetailsPage />} />
            <Route path="/servers/:serverId/:section" element={<ServerDetailsPage />} />
            <Route path="/protocol-settings" element={<ProtocolSettingsPage />} />
            <Route path="/protocol-settings/:serverId" element={<ProtocolSettingsPage />} />
            <Route path="/vpn-accounts" element={<VpnAccountsPage />} />
            <Route path="/vpn-accounts/:accountId" element={<VpnAccountsPage />} />
            <Route path="/vpn-accounts/:accountId/:section" element={<VpnAccountsPage />} />
            <Route path="/routing-profiles" element={<RoutingProfilesPage />} />
            <Route path="/routing-profiles/:profileId" element={<RoutingProfilesPage />} />
            <Route path="/routing-profiles/:profileId/:section" element={<RoutingProfilesPage />} />
            <Route path="/node-groups" element={<NodeGroupsPage />} />
            <Route path="/node-groups/:groupId" element={<NodeGroupsPage />} />
            <Route path="/settings/security" element={<SecurityPage />} />
            <Route path="/settings" element={<SettingsPage />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </main>

        <footer className="admin-statusbar">
          <span>{t('app.footerProduct')}</span>
          <span>{t('app.version')}</span>
          <strong><span className="status-dot status-dot-ok" /> {t('dashboard.systemsOperational')}</strong>
          <span className="admin-statusbar-time">{t('dashboard.serverTime', { time: formatServerTime(managerHealthQuery.data?.timestamp, serverTimeElapsed) })}</span>
        </footer>
      </div>
    </div>
  );
}

export function App() {
  const location = useLocation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [authToken, setAuthTokenState] = useState<string | null>(() => getAuthToken());
  useLocale();

  const isPortalRoute = location.pathname.startsWith('/portal');
  const isLoginRoute = location.pathname.startsWith('/login');
  const isSetupRoute = location.pathname.startsWith('/setup');

  const sessionQuery = useQuery({
    queryKey: ['admin-session', authToken],
    queryFn: getMe,
    enabled: Boolean(authToken) && !isPortalRoute && !isSetupRoute,
    retry: false,
  });
  const sessionIsUnauthorized = sessionQuery.isError
    && sessionQuery.error instanceof ApiError && sessionQuery.error.status === 401;

  const logoutMutation = useMutation({
    mutationFn: logout,
    onSettled: () => {
      clearAuthToken();
      setAuthTokenState(null);
      queryClient.removeQueries({ queryKey: ['admin-session'] });
      queryClient.removeQueries({ queryKey: ['me'] });
      queryClient.removeQueries({ queryKey: transferReservationsKey });
      navigate('/login', { replace: true });
    },
  });

  useEffect(() => {
    if (!sessionIsUnauthorized) {
      return;
    }

    clearAuthToken();
    setAuthTokenState(null);
    queryClient.removeQueries({ queryKey: ['admin-session'] });
    queryClient.removeQueries({ queryKey: ['me'] });
    queryClient.removeQueries({ queryKey: transferReservationsKey });
  }, [queryClient, sessionIsUnauthorized]);

  const handleLogin = () => {
    setAuthTokenState(getAuthToken());
  };
  const sessionNotice = (
    <section className="auth-session-recovery" aria-label={t('auth.retrySessionCheck')}>
      <div role="alert" className="form-message auth-message auth-message-error">
        {t('auth.sessionCheckUnavailable')}
      </div>
      <button className="primary-button" type="button" disabled={sessionQuery.isFetching}
        onClick={() => { void sessionQuery.refetch(); }}>
        {t(sessionQuery.isFetching ? 'auth.checkingSession' : 'auth.retrySessionCheck')}
      </button>
    </section>
  );

  if (location.pathname.startsWith('/portal')) {
    return <PortalShell />;
  }

  if (isSetupRoute) {
    return (
      <AuthShell>
        <SetupPage onLogin={handleLogin} />
      </AuthShell>
    );
  }

  if (authToken && sessionQuery.isError && !sessionIsUnauthorized && (!sessionQuery.data || isLoginRoute)) {
    return (
      <AuthShell>
        {sessionNotice}
      </AuthShell>
    );
  }

  if (isLoginRoute) {
    if (authToken && sessionQuery.isSuccess) {
      return <Navigate to="/" replace />;
    }

    if (authToken && sessionQuery.isPending) {
      return (
        <AuthShell>
          <p className="empty-state">{t('auth.checkingSession')}</p>
        </AuthShell>
      );
    }

    return <AuthShell onLogin={handleLogin} />;
  }

  if (!authToken) {
    return <Navigate to="/login" replace />;
  }

  if (sessionQuery.isPending) {
    return (
      <AuthShell>
        <p className="empty-state">{t('auth.checkingSession')}</p>
      </AuthShell>
    );
  }

  if (sessionIsUnauthorized) {
    return <Navigate to="/login" replace />;
  }

  return <AdminShell isLoggingOut={logoutMutation.isPending} onLogout={() => logoutMutation.mutate()}
    user={sessionQuery.data?.user} sessionNotice={sessionQuery.isError ? sessionNotice : undefined} />;
}
