import { isTransferReservationError } from '../../entities/vpnAccount/api/vpnAccountTransferApi';
import { TransferReservationNotice } from './TransferReservationNotice';
import { type FormEvent, useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Navigate, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { getServers } from '../../entities/server/api/serverApi';
import { getVpnAccount } from '../../entities/vpnAccount/api/vpnAccountManagementApi';
import { createVpnAccount } from '../../entities/vpnAccount/api/vpnAccountApi';
import { t } from '../../shared/i18n/i18n';
import { EmptyState } from '../../shared/ui/EmptyState';
import { StatusBadge } from '../../shared/ui/StatusBadge';
import { WorkspaceNav } from '../../shared/ui/WorkspaceNav';
import { AccountOverview } from './AccountOverview';
import { AccessDevicesPanel } from './AccessDevicesPanel';
import { TrafficStatsPanel } from './TrafficStatsPanel';
import { VpnAccountConnectionPanels } from './VpnAccountConnectionPanels';
import { VpnAccountManagementList } from './VpnAccountManagementList';
import { VpnAccountManagementPanel } from './VpnAccountManagementPanel';
import { VpnAccountRoutingPolicyPanel } from './VpnAccountRoutingPolicyPanel';
import { getVpnAccountManagementCopy } from './vpnAccountManagementCopy';
import { accountSections, accountWorkspaceHref, isAccountSection } from './accountWorkspace';
import './vpnAccountManagement.css';
import './vpnAccountNotes.css';


function getErrorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message.trim() !== '' ? error.message : fallback;
}

export function VpnAccountsPage() {
  const copy = getVpnAccountManagementCopy();
  const { accountId, section } = useParams<{ accountId: string; section: string }>();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const queryClient = useQueryClient();
  const [isCreateOpen, setIsCreateOpen] = useState(searchParams.get('create') === '1');
  const [displayName, setDisplayName] = useState('');
  const [email, setEmail] = useState('');
  const [phone, setPhone] = useState('');
  const [telegramUsername, setTelegramUsername] = useState('');
  // A create link opened for a node (?create=1&server=<id>, e.g. from Getting
  // Started) keeps that node selected; the list is filtered by the same node.
  const [serverId, setServerId] = useState(() => (searchParams.get('create') === '1' ? searchParams.get('server') ?? '' : ''));

  useEffect(() => {
    if (searchParams.get('create') !== '1') return;
    setIsCreateOpen(true);
    const requestedServer = searchParams.get('server');
    if (requestedServer) setServerId((current) => current || requestedServer);
  }, [searchParams]);

  const serversQuery = useQuery({ queryKey: ['servers'], queryFn: getServers });

  useEffect(() => {
    const servers = serversQuery.data?.items;
    // Never submit a node that does not exist (a stale or hand-edited link).
    if (servers && serverId && !servers.some((server) => server.id === serverId)) setServerId('');
  }, [serversQuery.data, serverId]);
  const accountQuery = useQuery({
    queryKey: ['vpn-account', accountId],
    queryFn: () => getVpnAccount(accountId ?? ''),
    enabled: Boolean(accountId),
  });

  const createAccountMutation = useMutation({
    mutationFn: () => createVpnAccount({
      displayName: displayName.trim(),
      email: email.trim() || undefined,
      phone: phone.trim() || undefined,
      telegramUsername: telegramUsername.trim() || undefined,
      serverId: serverId || undefined,
    }),
    onSuccess: async (account) => {
      setDisplayName('');
      setEmail('');
      setPhone('');
      setTelegramUsername('');
      setServerId('');
      setIsCreateOpen(false);
      const nextParams = new URLSearchParams(searchParams);
      nextParams.delete('create');
      nextParams.delete('page');
      // New accounts may not be in the node's applied configuration yet.
      // Start with the readiness-aware next action, not a premature device link.
      nextParams.delete('addDevice');
      await queryClient.invalidateQueries({ queryKey: ['vpn-accounts'] });
      const query = nextParams.toString();
      navigate(`/vpn-accounts/${encodeURIComponent(account.id)}/overview${query ? `?${query}` : ''}`);
    },
  });

  const canCreateAccount = displayName.trim() !== '';

  function openCreateForm() {
    setIsCreateOpen(true);
    setSearchParams((current) => {
      const next = new URLSearchParams(current);
      next.set('create', '1');
      return next;
    }, { replace: true });
  }

  function closeCreateForm() {
    setIsCreateOpen(false);
    setSearchParams((current) => {
      const next = new URLSearchParams(current);
      next.delete('create');
      return next;
    }, { replace: true });
  }

  function handleCreateAccount(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (canCreateAccount) createAccountMutation.mutate();
  }

  if (accountId && (!section || !isAccountSection(section))) {
    const target = section ? 'overview' : searchParams.get('addDevice') === '1' ? 'access' : 'overview';
    const query = searchParams.toString();
    return <Navigate to={`/vpn-accounts/${encodeURIComponent(accountId)}/${target}${query ? `?${query}` : ''}`} replace />;
  }

  const navigation = accountId ? accountSections.map((item) => ({
    href: accountWorkspaceHref(accountId, item, searchParams),
    label: t(`accountWorkspace.${item}`),
  })) : [];

  return (
    <section className="page vpn-accounts-page feature-screen-page vpn-account-management-page">
      <div className="page-header feature-page-header">
        <div>
          <h1>{t('vpnAccounts.title')}</h1>
          <p>{copy.pageSubtitle}</p>
        </div>
      </div>

      {isCreateOpen && (
        <div className="panel vpn-account-create-panel">
          <div className="panel-header">
            <div>
              <div className="panel-title">{t('vpnAccounts.createAction')}</div>
              <p className="panel-subtitle">{t('vpnAccounts.emptyDescription')}</p>
            </div>
          </div>
          <form className="vpn-account-create-form" onSubmit={handleCreateAccount}>
            <div className="vpn-account-create-grid">
              <label className="field">
                <span>{copy.accountName}</span>
                <input
                  value={displayName}
                  onChange={(event) => setDisplayName(event.target.value)}
                  placeholder={copy.accountNamePlaceholder}
                />
                <small>{copy.accountNameHint}</small>
              </label>
              <label className="field">
                <span>{t('vpnAccounts.email')}</span>
                <input
                  type="email"
                  value={email}
                  onChange={(event) => setEmail(event.target.value)}
                  placeholder={t('vpnAccounts.emailPlaceholder')}
                />
              </label>
              <label className="field">
                <span>{t('vpnAccounts.phone')}</span>
                <input
                  type="tel"
                  value={phone}
                  onChange={(event) => setPhone(event.target.value)}
                  placeholder={t('vpnAccounts.phonePlaceholder')}
                />
              </label>
              <label className="field">
                <span>{t('vpnAccounts.telegramUsername')}</span>
                <input
                  value={telegramUsername}
                  onChange={(event) => setTelegramUsername(event.target.value)}
                  placeholder={t('vpnAccounts.telegramUsernamePlaceholder')}
                />
              </label>
              <label className="field">
                <span>{t('vpnAccounts.serverAssignment')}</span>
                <select value={serverId} disabled={createAccountMutation.isPending} onChange={(event) => {
                  setServerId(event.target.value);
                  createAccountMutation.reset();
                }}>
                  <option value="">{t('vpnAccounts.noServerAssignment')}</option>
                  {(serversQuery.data?.items ?? []).map((server) => (
                    <option value={server.id} key={server.id}>{server.name || server.id}</option>
                  ))}
                </select>
              </label>
            </div>
            {createAccountMutation.isError && (isTransferReservationError(createAccountMutation.error) ?
              <TransferReservationNotice nodeIds={[serverId]} blocked /> : (
              <div className="form-message form-message-error">
                {getErrorMessage(createAccountMutation.error, t('vpnAccounts.createError'))}
              </div>
            ))}
            <div className="form-actions">
              <button className="primary-button" type="submit" disabled={!canCreateAccount || createAccountMutation.isPending}>
                {createAccountMutation.isPending ? t('vpnAccounts.creating') : t('vpnAccounts.createAction')}
              </button>
              <button className="small-button" type="button" onClick={closeCreateForm}>{t('common.cancel')}</button>
            </div>
          </form>
        </div>
      )}

      <div className="vpn-account-management-layout">
        <VpnAccountManagementList onCreate={openCreateForm} />

        <div
          className="vpn-account-management-detail-stack"
          key={accountId}
          data-route-scroll-target={accountId ? '' : undefined}
          role={accountId ? 'region' : undefined}
          aria-labelledby={accountId ? 'vpn-account-workspace-title' : undefined}
          tabIndex={accountId ? -1 : undefined}
        >
          {!accountId ? (
            <div className="panel feature-detail-panel vpn-account-management-panel">
              <EmptyState title={t('vpnAccounts.selectTitle')} description={t('vpnAccounts.selectDescription')} />
            </div>
          ) : (
            <>
              <div className="vpn-account-workspace-context">
                <div className="vpn-account-workspace-header panel">
                  <div>
                    <span className="vpn-account-workspace-eyebrow">{t('accountWorkspace.account')}</span>
                    <h2 id="vpn-account-workspace-title">{accountQuery.data?.displayName || (accountQuery.isLoading ? t('common.loading') : accountId)}</h2>
                    <span className="vpn-account-workspace-id">{accountId}</span>
                  </div>
                  {accountQuery.data && <StatusBadge status={accountQuery.data.status} />}
                </div>
                <WorkspaceNav label={t('accountWorkspace.navigation')} items={navigation} />
              </div>
              {accountQuery.isError ? (
                <div className="panel form-message form-message-error">{copy.loadAccountError}</div>
              ) : section === 'overview' ? (
                accountQuery.data ? <AccountOverview account={accountQuery.data} /> : <div className="panel vpn-account-workspace-wait">{t('common.loading')}</div>
              ) : section === 'access' ? <AccessDevicesPanel accountId={accountId} />
                : null}
              <div hidden={section !== 'protocols' || accountQuery.isError}>
                <VpnAccountConnectionPanels accountId={accountId} active={section === 'protocols' && !accountQuery.isError} />
              </div>
              <div hidden={section !== 'routing' || accountQuery.isError}>
                <VpnAccountRoutingPolicyPanel accountId={accountId} active={section === 'routing' && !accountQuery.isError} />
              </div>
              <div hidden={section !== 'traffic' || accountQuery.isError}>
                <TrafficStatsPanel accountId={accountId} />
              </div>
              <div hidden={section !== 'settings' || accountQuery.isError}>
                <VpnAccountManagementPanel accountId={accountId} />
              </div>
            </>
          )}
        </div>
      </div>
    </section>
  );
}
