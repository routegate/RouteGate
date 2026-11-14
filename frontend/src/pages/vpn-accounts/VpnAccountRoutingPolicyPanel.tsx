import { transferReservationsKey } from '../../entities/vpnAccount/api/vpnAccountTransferApi';
import { type FormEvent, useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { getNodeGroups } from '../../entities/nodeGroup/api/nodeGroupApi';
import { getRoutingProfiles } from '../../entities/routingProfile/api/routingProfileApi';
import { getServers } from '../../entities/server/api/serverApi';
import {
  assignVpnAccountNodeGroup,
  assignVpnAccountRoutingProfile,
  applyVpnAccountAutomaticSelection,
  clearVpnAccountNodeGroup,
  clearVpnAccountRoutingProfile,
  getVpnAccountRoutingPolicy,
  previewVpnAccountAutomaticSelection,
  updateVpnAccountAutomaticSelection,
  type RoutingProfileSource,
} from '../../entities/vpnAccount/api/vpnAccountApi';
import { getVpnAccount, updateVpnAccount } from '../../entities/vpnAccount/api/vpnAccountManagementApi';
import { getCurrentLocale, t } from '../../shared/i18n/i18n';
import { Section } from '../../shared/ui/Section';
import './vpnAccountRoutingPolicy.css';
import { getAccountTransfer, startAccountTransfer } from '../../entities/vpnAccount/api/vpnAccountTransferApi';
import { AccountTransferPanel } from './AccountTransferPanel';
import { isTransferReservationError } from '../../entities/vpnAccount/api/vpnAccountTransferApi';
import { TransferReservationNotice } from './TransferReservationNotice';
import { accountTransferError } from './accountTransferMessages';

function selectionStatusLabel(status: 'selected' | 'current' | 'no_eligible_candidates' | 'node_group_required' | 'cooldown'): string {
  switch (status) {
    case 'selected': return t('automaticSelection.status.selected');
    case 'current': return t('automaticSelection.status.current');
    case 'no_eligible_candidates': return t('automaticSelection.status.no_eligible_candidates');
    case 'node_group_required': return t('automaticSelection.status.node_group_required');
    case 'cooldown': return t('automaticSelection.status.cooldown');
  }
}

function sourceLabel(source: RoutingProfileSource): string {
  switch (source) {
    case 'account': return t('routingPolicy.sourceAccount');
    case 'server': return t('routingPolicy.sourceServer');
    case 'default': return t('routingPolicy.sourceDefault');
    default: return t('routingPolicy.sourceNone');
  }
}

function protocolDisplayName(protocol: string): string {
  switch (protocol.trim().toLowerCase()) {
    case 'vless': return 'VLESS / Reality';
    case 'wireguard': return 'WireGuard';
    case 'hysteria2': return 'Hysteria2';
    case 'shadowsocks': return 'Shadowsocks 2022';
    case 'mtproto': return 'MTProto / FakeTLS';
    default: return protocol;
  }
}

function getCopy() {
  if (getCurrentLocale() === 'ru') {
    return {
      unsavedNodeGroup: 'Группа узлов изменена, но ещё не сохранена.',
      saveNodeGroupFirst: 'Сначала сохраните выбранную группу узлов. Предпросмотр и применение используют только сохранённую группу.',
      savePolicyFirst: 'Настройки автоматического выбора изменены. Сначала сохраните их, чтобы предпросмотр и применение использовали именно эти значения.',
      placementTitle: 'Размещение',
      placementSubtitle: 'На каком узле обслуживается этот аккаунт.',
      currentNode: 'Текущий узел',
      saveNode: 'Назначить узел',
      prepareTransfer: 'Подготовить перенос',
      nodeSaved: 'Узел обновлён.',
      nodeSaveError: 'Не удалось назначить узел.',
      configNotice: 'Изменение затронуло конфигурацию VPN-сервера и требует нового развёртывания.',
      nodeSwitchWarning: 'Для активного аккаунта начнётся управляемый перенос VLESS/Reality. Новый узел будет подготовлен до переключения подписки. Ссылка сохранится. После переключения проверьте обновление подписок и подключения пользователей, затем подтвердите очистку старого узла.',
      nodeSwitchConfirm: 'Подготовить новый узел для переноса? Текущая подписка продолжит работать до отдельного переключения.',
      openDeploy: 'Открыть развёртывание конфигов →',
    } as const;
  }

  return {
    unsavedNodeGroup: 'The node group has changed but is not saved yet.',
    saveNodeGroupFirst: 'Save the selected node group first. Preview and Apply use only the persisted group.',
    savePolicyFirst: 'Automatic-selection settings have changed. Save them first so Preview and Apply use these exact values.',
    placementTitle: 'Placement',
    placementSubtitle: 'Which node currently serves this account.',
    currentNode: 'Current node',
    saveNode: 'Assign node',
    prepareTransfer: 'Prepare transfer',
    nodeSaved: 'Node updated.',
    nodeSaveError: 'Failed to assign the node.',
    configNotice: 'The change affected a VPN server configuration and requires a new deployment.',
    nodeSwitchWarning: 'An active account starts a managed VLESS/Reality transfer. The target is prepared before subscription cutover. The link stays unchanged. Verify client refresh and connections before acknowledging source cleanup.',
    nodeSwitchConfirm: 'Prepare the target for transfer? The current subscription remains active until a separate cutover.',
    openDeploy: 'Open Config Deploy →',
  } as const;
}

export function VpnAccountRoutingPolicyPanel({ accountId, active = true }: { accountId: string; active?: boolean }) {
  const queryClient = useQueryClient();
  const copy = getCopy();
  const [routingProfileId, setRoutingProfileId] = useState('');
  const [nodeGroupId, setNodeGroupId] = useState('');
  const [automaticSelectionEnabled, setAutomaticSelectionEnabled] = useState(false);
  const [allowDegraded, setAllowDegraded] = useState(false);
  const [cooldownSeconds, setCooldownSeconds] = useState(300);
  const [serverId, setServerId] = useState('');
  const [nodeMessage, setNodeMessage] = useState('');
  const [nodeError, setNodeError] = useState('');
  const [nodeConfigChanged, setNodeConfigChanged] = useState(false);
  const [edited, setEdited] = useState({ node: false, profile: false, group: false, selection: false });

  const policyQuery = useQuery({
    queryKey: ['vpn-account-routing-policy', accountId],
    queryFn: () => getVpnAccountRoutingPolicy(accountId),
  });
  const accountQuery = useQuery({
    queryKey: ['vpn-account', accountId],
    queryFn: () => getVpnAccount(accountId),
  });
  const transferQuery = useQuery({ queryKey: ['vpn-account-transfer', accountId], queryFn: () => getAccountTransfer(accountId), enabled: active, refetchInterval: active ? 5000 : false });
  const transfer = transferQuery.data?.transfer;
  const transferActive = Boolean(transfer && !transfer.completedAt);
  const profilesQuery = useQuery({ queryKey: ['routing-profiles'], queryFn: getRoutingProfiles });
  const groupsQuery = useQuery({ queryKey: ['node-groups'], queryFn: getNodeGroups });
  const serversQuery = useQuery({ queryKey: ['servers'], queryFn: getServers });

  useEffect(() => {
    if (!accountQuery.data || edited.node) return;
    setServerId(accountQuery.data?.serverId ?? '');
  }, [accountQuery.data?.serverId, edited.node]);

  const savedNodeGroupId = policyQuery.data?.nodeGroup?.id ?? '';
  const savedSelectionPolicy = policyQuery.data?.automaticSelectionPolicy;
  const nodeGroupDirty = Boolean(policyQuery.data) && nodeGroupId !== savedNodeGroupId;
  const selectionPolicyDirty = Boolean(savedSelectionPolicy)
    && (automaticSelectionEnabled !== savedSelectionPolicy?.enabled
      || allowDegraded !== savedSelectionPolicy?.allowDegraded
      || cooldownSeconds !== savedSelectionPolicy?.cooldownSeconds);
  const automaticSelectionDirty = nodeGroupDirty || selectionPolicyDirty;

  const selectionPreviewQuery = useQuery({
    queryKey: ['vpn-account-automatic-selection-preview', accountId],
    queryFn: () => previewVpnAccountAutomaticSelection(accountId),
    enabled: active && Boolean(policyQuery.data?.nodeGroup) && !automaticSelectionDirty,
  });

  useEffect(() => {
    if (!policyQuery.data) return;
    if (!edited.profile) setRoutingProfileId(policyQuery.data.explicitRoutingProfile?.id ?? '');
    if (!edited.group) setNodeGroupId(policyQuery.data.nodeGroup?.id ?? '');
    if (!edited.selection) {
      setAutomaticSelectionEnabled(policyQuery.data.automaticSelectionPolicy.enabled ?? false);
      setAllowDegraded(policyQuery.data.automaticSelectionPolicy.allowDegraded ?? false);
      setCooldownSeconds(policyQuery.data.automaticSelectionPolicy.cooldownSeconds ?? 300);
    }
  }, [policyQuery.data, edited.profile, edited.group, edited.selection]);

  async function finishPolicySave(field: 'profile' | 'group' | 'selection') {
    await refreshPolicy();
    if (queryClient.getQueryState(['vpn-account-routing-policy', accountId])?.status === 'success') {
      setEdited(current => ({ ...current, [field]: false }));
    }
  }

  async function refreshPolicy() {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['vpn-account-routing-policy', accountId] }),
      queryClient.invalidateQueries({ queryKey: ['vpn-account-transfer', accountId] }),
      queryClient.invalidateQueries({ queryKey: transferReservationsKey }),
      queryClient.invalidateQueries({ queryKey: ['vpn-account-client-connection', accountId] }),
      queryClient.invalidateQueries({ queryKey: ['vpn-accounts'] }),
      queryClient.invalidateQueries({ queryKey: ['vpn-account', accountId] }),
      queryClient.invalidateQueries({ queryKey: ['vpn-account-automatic-selection-preview', accountId] }),
    ]);
  }

  const profileMutation = useMutation({
    mutationFn: () => routingProfileId
      ? assignVpnAccountRoutingProfile(accountId, routingProfileId)
      : clearVpnAccountRoutingProfile(accountId),
    onSuccess: () => finishPolicySave('profile'),
  });
  const groupMutation = useMutation({
    mutationFn: () => nodeGroupId
      ? assignVpnAccountNodeGroup(accountId, nodeGroupId)
      : clearVpnAccountNodeGroup(accountId),
    onSuccess: () => finishPolicySave('group'),
  });
  const selectionPolicyMutation = useMutation({
    mutationFn: () => updateVpnAccountAutomaticSelection(accountId, {
      enabled: automaticSelectionEnabled,
      allowDegraded,
      cooldownSeconds,
    }),
    onSuccess: () => finishPolicySave('selection'),
  });
  const selectionApplyMutation = useMutation({
    mutationFn: () => applyVpnAccountAutomaticSelection(accountId),
    onSuccess: refreshPolicy,
  });
  const nodeMutation = useMutation({
    mutationFn: async () => {
      if (transferQuery.data?.requiresTransfer && accountQuery.data?.serverId) {
        await startAccountTransfer(accountId, serverId);
        return { staged: true, serverId: accountQuery.data.serverId };
      }
      return { ...(await updateVpnAccount(accountId, { serverId })), staged: false };
    },
    onSuccess: async (updated) => {
      // The transfer panel owns staged progress; a static success message becomes stale.
      setNodeMessage(updated.staged ? '' : copy.nodeSaved);
      setNodeError('');
      setNodeConfigChanged(!updated.staged);
      setServerId(updated.serverId ?? '');
      await refreshPolicy();
      if (queryClient.getQueryState(['vpn-account', accountId])?.status === 'success') {
        setEdited(current => ({ ...current, node: false }));
      }
    },
    onError: (error) => {
      setNodeMessage('');
      setNodeError(accountTransferError(error));
    },
  });

  function saveNode(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (editingDisabled || serverId === (accountQuery.data?.serverId ?? '')) return;
    if (transferQuery.data?.requiresTransfer && !window.confirm(copy.nodeSwitchConfirm)) return;
    nodeMutation.mutate();
  }

  function saveProfile(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (editingDisabled) return;
    profileMutation.mutate();
  }

  function saveGroup(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (editingDisabled) return;
    groupMutation.mutate();
  }

  function saveSelectionPolicy(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (editingDisabled || !policyQuery.data?.nodeGroup || nodeGroupDirty) return;
    selectionPolicyMutation.mutate();
  }

  const policy = policyQuery.data;
  const hasError = transferQuery.isError || policyQuery.isError || profilesQuery.isError || groupsQuery.isError
    || accountQuery.isError || serversQuery.isError;
  const isLoading = (active && transferQuery.isLoading) || policyQuery.isLoading || profilesQuery.isLoading || groupsQuery.isLoading
    || accountQuery.isLoading || serversQuery.isLoading;
  const editingDisabled = transferActive || isLoading || hasError || nodeMutation.isPending || profileMutation.isPending
    || groupMutation.isPending || selectionPolicyMutation.isPending || selectionApplyMutation.isPending;
  const currentServerName = serversQuery.data?.items.find((server) => server.id === accountQuery.data?.serverId)?.name
    ?? accountQuery.data?.serverId
    ?? t('vpnAccounts.noServerAssignment');

  if (hasError || isLoading) {
    return (
      <Section title={t('routingPolicy.title')} description={t('routingPolicy.subtitle')}>
        {hasError
          ? <div className="form-message form-message-error">{t('routingPolicy.loadError')}</div>
          : <p className="empty-state">{t('common.loading')}</p>}
      </Section>
    );
  }

  return (
    <div className="vpn-account-routing-workspace">
      {(profileMutation.isError || groupMutation.isError || selectionPolicyMutation.isError || selectionApplyMutation.isError) && <div className="form-message form-message-error">{t('routingPolicy.saveError')}</div>}

      <Section title={copy.placementTitle} description={copy.placementSubtitle}>
        {transfer && <AccountTransferPanel key={transfer.id} accountId={accountId} transfer={transfer} serverNames={Object.fromEntries((serversQuery.data?.items ?? []).map(s => [s.id, s.name || s.id]))} />}
        <form className="vpn-account-routing-form" onSubmit={saveNode} onChange={() => {
          setEdited(current => ({ ...current, node: true }));
          setNodeMessage('');
          setNodeError('');
        }}>
          <div className="routing-policy-effective">
            <span>{copy.currentNode}</span>
            <strong>{currentServerName}</strong>
          </div>
          <label className="field">
            <span>{t('vpnAccounts.serverAssignment')}</span>
            <select value={serverId} disabled={editingDisabled} onChange={(event) => setServerId(event.target.value)}>
              <option value="">{t('vpnAccounts.noServerAssignment')}</option>
              {(serversQuery.data?.items ?? []).map((server) => (
                <option key={server.id} value={server.id}>{server.name || server.id}</option>
              ))}
            </select>
          </label>
          {serverId !== (accountQuery.data?.serverId ?? '') && (
            <div className="form-message form-message-warning" role="alert">{copy.nodeSwitchWarning}</div>
          )}
          <div className="form-actions">
            <button className="small-button" type="submit" disabled={editingDisabled || serverId === (accountQuery.data?.serverId ?? '')}>{transferQuery.data?.requiresTransfer ? copy.prepareTransfer : copy.saveNode}</button>
          </div>
          {nodeMessage && <div className="form-message form-message-success">{nodeMessage}</div>}
          {nodeError && (isTransferReservationError(nodeMutation.error) ?
            <TransferReservationNotice nodeIds={[serverId, accountQuery.data?.serverId ?? '']} blocked /> :
            <div className="form-message form-message-error">{nodeError}</div>)}
          {nodeConfigChanged && (
            <div className="form-message vpn-account-config-notice">
              <span>{copy.configNotice}</span>
              <Link className="text-link" to="/config-deploy">{copy.openDeploy}</Link>
            </div>
          )}
        </form>
      </Section>

      {policy && (
        <div className="vpn-account-routing-policy-grid">
          <Section title={t('routingPolicy.profile')}>
            <form className="vpn-account-routing-form" onSubmit={saveProfile} onChange={() => {
              setEdited(current => ({ ...current, profile: true }));
              profileMutation.reset();
            }}>
              <label className="field">
                <span>{t('routingPolicy.profile')}</span>
                <select value={routingProfileId} disabled={editingDisabled} onChange={(event) => setRoutingProfileId(event.target.value)}>
                  <option value="">{t('routingPolicy.inherit')}</option>
                  {(profilesQuery.data?.items ?? []).map((profile) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}
                </select>
              </label>
              <div className="routing-policy-effective">
                <span>{t('routingPolicy.effective')}</span>
                <strong>{policy.effectiveRoutingProfile?.name ?? t('common.notAvailable')}</strong>
                <small>{t('routingPolicy.source', { source: sourceLabel(policy.routingProfileSource) })}</small>
              </div>

              {!policy.clientRoutingSupported && <div className="form-message form-message-warning">{t('routingPolicy.clientRoutingUnsupported')}</div>}
              <div className="form-actions">
                <button className="small-button" type="submit" disabled={editingDisabled}>{routingProfileId ? t('routingPolicy.saveProfile') : t('routingPolicy.clearProfile')}</button>
              </div>
            </form>
          </Section>

          <Section title={t('routingPolicy.nodeGroup')}>
            <form className="vpn-account-routing-form" onSubmit={saveGroup} onChange={() => {
              setEdited(current => ({ ...current, group: true }));
              groupMutation.reset();
            }}>
              <label className="field">
                <span>{t('routingPolicy.nodeGroup')}</span>
                <select value={nodeGroupId} disabled={editingDisabled} onChange={(event) => setNodeGroupId(event.target.value)}>
                  <option value="">{t('routingPolicy.noNodeGroup')}</option>
                  {(groupsQuery.data?.items ?? []).map((group) => <option key={group.id} value={group.id}>{group.name} · {group.memberCount}</option>)}
                </select>
              </label>
              {policy.nodeGroup && !nodeGroupDirty && (
                <div className={`form-message ${policy.currentServerInGroup ? 'form-message-success' : 'form-message-warning'}`}>
                  {policy.currentServerInGroup ? t('routingPolicy.currentInGroup') : t('routingPolicy.currentOutsideGroup')}
                </div>
              )}
              {nodeGroupDirty && <div className="form-message form-message-warning">{copy.unsavedNodeGroup}</div>}
              <p className="routing-policy-automation-note">{t('automaticSelection.groupHelp')}</p>
              <div className="form-actions">
                <button className="small-button" type="submit" disabled={editingDisabled}>{nodeGroupId ? t('routingPolicy.saveGroup') : t('routingPolicy.clearGroup')}</button>
              </div>
            </form>
          </Section>

          <Section title={t('automaticSelection.title')} description={t('automaticSelection.subtitle')}>
            <form className="vpn-account-routing-form" onSubmit={saveSelectionPolicy} onChange={() => {
              setEdited(current => ({ ...current, selection: true }));
              selectionPolicyMutation.reset();
            }}>
              <label className="field checkbox-field">
                <input
                  type="checkbox"
                  checked={automaticSelectionEnabled}
                  disabled={editingDisabled || !policy.nodeGroup || nodeGroupDirty}
                  onChange={(event) => setAutomaticSelectionEnabled(event.target.checked)}
                />
                <span>{t('automaticSelection.enabled')}</span>
              </label>
              <label className="field checkbox-field">
                <input type="checkbox" checked={allowDegraded} disabled={editingDisabled || !policy.nodeGroup || nodeGroupDirty} onChange={(event) => setAllowDegraded(event.target.checked)} />
                <span>{t('automaticSelection.allowDegraded')}</span>
              </label>
              <label className="field">
                <span>{t('automaticSelection.cooldown')}</span>
                <input
                  type="number"
                  min={1}
                  max={1440}
                  value={Math.round(cooldownSeconds / 60)}
                  disabled={editingDisabled || !policy.nodeGroup || nodeGroupDirty}
                  onChange={(event) => setCooldownSeconds(Math.max(60, Number(event.target.value || 1) * 60))}
                />
              </label>
              <div className="form-actions">
                <button className="small-button" type="submit" disabled={editingDisabled || !policy.nodeGroup || nodeGroupDirty}>{t('automaticSelection.save')}</button>
                <button
                  className="small-button secondary"
                  type="button"
                  disabled={editingDisabled || !policy.nodeGroup || automaticSelectionDirty || selectionPreviewQuery.isFetching}
                  onClick={() => selectionPreviewQuery.refetch()}
                >
                  {t('automaticSelection.refresh')}
                </button>
              </div>
              {!policy.nodeGroup && !nodeGroupDirty && <div className="form-message form-message-warning">{t('automaticSelection.nodeGroupRequired')}</div>}
              {nodeGroupDirty && <div className="form-message form-message-warning">{copy.saveNodeGroupFirst}</div>}
              {!nodeGroupDirty && selectionPolicyDirty && <div className="form-message form-message-warning">{copy.savePolicyFirst}</div>}
              {!automaticSelectionDirty && selectionPreviewQuery.isError && <div className="form-message form-message-error">{t('automaticSelection.previewError')}</div>}
              {!automaticSelectionDirty && selectionPreviewQuery.data && (
                <div className="automatic-selection-decision">
                  <span>{selectionStatusLabel(selectionPreviewQuery.data.status)}</span>
                  {selectionPreviewQuery.data.selectedCandidate && (
                    <strong>{selectionPreviewQuery.data.selectedCandidate.serverName} · {protocolDisplayName(selectionPreviewQuery.data.selectedCandidate.protocol)}</strong>
                  )}
                  <small>{t('automaticSelection.eligible', { count: selectionPreviewQuery.data.eligibleCandidates })}</small>
                  {selectionPreviewQuery.data.blockedUntil && <small>{t('automaticSelection.blockedUntil', { date: new Date(selectionPreviewQuery.data.blockedUntil).toLocaleString() })}</small>}
                </div>
              )}
              {selectionApplyMutation.data?.configDeploymentRequired && (
                <div className="automatic-selection-next-action">
                  <div className="form-message form-message-warning">{t('automaticSelection.deployRequired')}</div>
                  <div className="form-actions">
                    {selectionApplyMutation.data.affectedServerIds.map((serverId, index) => (
                      <Link className="small-button" key={serverId} to={`/servers/${encodeURIComponent(serverId)}`}>
                        {index === 0 ? t('automaticSelection.openSelectedNode') : t('automaticSelection.openPreviousNode')}
                      </Link>
                    ))}
                  </div>
                </div>
              )}
              <div className="form-actions">
                <button
                  className="small-button"
                  type="button"
                  disabled={editingDisabled || automaticSelectionDirty || !selectionPreviewQuery.data?.canApply}
                  onClick={() => {
                    if (!editingDisabled && !automaticSelectionDirty && selectionPreviewQuery.data?.canApply &&
                      window.confirm(copy.nodeSwitchConfirm)) selectionApplyMutation.mutate();
                  }}
                >
                  {t('automaticSelection.apply')}
                </button>
              </div>
            </form>
          </Section>
        </div>
      )}
    </div>
  );
}
