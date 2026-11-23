import { isTransferReservationError } from '../../entities/vpnAccount/api/vpnAccountTransferApi';
import { TransferReservationNotice } from './TransferReservationNotice';
import { useState } from 'react';
import { useIsMutating, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { getProtocolSettings, getServers } from '../../entities/server/api/serverApi';
import type { ProtocolSettingsResponse } from '../../entities/server/api/serverApi';
import {
  getVpnAccountClientProfileState,
  updateVpnAccountClientProfile,
  type ClientProtocol,
  type ClientProtocolPreference,
  type UpdateVpnClientProfileRequest,
} from '../../entities/vpnAccount/api/vpnAccountApi';
import { getVpnAccount } from '../../entities/vpnAccount/api/vpnAccountManagementApi';
import { getCurrentLocale } from '../../shared/i18n/i18n';
import { Section } from '../../shared/ui/Section';
import {
  deployPendingProtocol,
  ensureProtocolRuntime,
  type ProtocolDeploymentStage,
} from './protocolDeploymentWorkflow';
import {
  activationConfirmed,
  canApplyProtocolSet,
  ordered,
  protocolOrder,
  protocolPreferenceView,
  sameProtocols,
} from './protocolPreferenceModel';
import { VpnAccountPreImportPanel } from './VpnAccountPreImportPanel';
import './multi-protocol-access.css';

export type ProtocolSelectionDraft = {
  primary: ClientProtocolPreference;
  enabledProtocols: ClientProtocol[];
};

type Props = {
  accountId: string;
  active: boolean;
  draft: ProtocolSelectionDraft | null;
  onDraftChange: (draft: ProtocolSelectionDraft | null) => void;
};

type MultiProtocolUpdate = UpdateVpnClientProfileRequest & {
  enabledProtocols: ClientProtocol[];
};

function getCopy() {
  if (getCurrentLocale() === 'ru') {
    return {
      title: 'Протоколы подключения',
      subtitle: 'Один VPN-аккаунт может использовать несколько протоколов одновременно. Добавление нового протокола не отключает уже работающие подключения.',
      enabled: 'Разрешённые протоколы',
      primary: 'Основной протокол',
      primaryHint: 'Основной протокол используется как вариант по умолчанию в местах, где нужен один способ подключения. Остальные разрешённые протоколы продолжают работать параллельно.',
      active: 'Активны сейчас',
      desired: 'Будут активны после применения',
      disabled: 'Не разрешён',
      details: 'Подробнее',
      nodeReady: 'VPN-узел готов к этому протоколу.',
      applyTitle: 'Основной протокол и применение',
      applySubtitle: 'Выберите основной протокол и примените выбранный набор.',
      auto: 'Auto — протокол узла по умолчанию',
      vless: 'VLESS / Reality', wireguard: 'WireGuard', hysteria2: 'Hysteria2',
      shadowsocks: 'Shadowsocks 2022', mtproto: 'MTProto / FakeTLS',
      safety: 'Рабочие подключения сохраняются, пока новый набор не будет успешно применён.',
      save: 'Применить набор протоколов', retry: 'Повторить применение', saving: 'Подготовка...',
      saved: 'Набор протоколов успешно применён.',
      pending: 'Новый набор сохранён как желаемый, но ещё не активирован. Предыдущие рабочие подключения сохранены.',
      loading: 'Загрузка протоколов...', loadError: 'Не удалось загрузить настройки протоколов.',
      saveError: 'Не удалось применить набор. Предыдущие активные протоколы должны остаться рабочими.',
      noServer: 'Сначала назначьте аккаунту VPN-узел.',
      selectOne: 'Должен быть выбран хотя бы один протокол.',
      autoNeedsDefault: 'Для Auto протокол узла по умолчанию должен входить в разрешённый набор.',
      setupRequired: 'Требуется настройка',
      protocolsNotReady: 'Сначала настройте выбранные протоколы на назначенном VPN-узле:',
      configureNode: 'Открыть настройки протоколов узла',
      settingsLoading: 'Дождитесь загрузки настроек назначенного VPN-узла.',
      settingsLoadError: 'Не удалось проверить готовность протоколов назначенного VPN-узла.',
      hysteria2NotReady: 'Для Hysteria2 необходимы отдельный TLS-домен и email для ACME.',
      errorDetail: 'Причина',
      awaitingApply: 'Клиентский доступ для этого аккаунта ещё не развёрнут на узле. Ссылки и подписка появятся после успешного применения.',
      awaitingFirstApply: 'На узле ещё нет ни одной успешно применённой конфигурации. Ссылки и подписка появятся после первого успешного применения.',
      applyAction: 'Нажмите «Применить набор протоколов»: панель сформирует конфигурацию узла и передаст её Agent. Также это можно сделать в разделе конфигураций узла.',
      savedSet: 'Сохранённый набор',
      servedSet: 'Фактически активен',
      unavailable: 'Клиентское подключение сейчас недоступно.',
      stages: {
        saving_preference: 'Сохраняю желаемый набор…', checking_runtime: 'Проверяю VPN runtimes…',
        installing_runtime: 'Устанавливаю необходимый runtime…', rendering_config: 'Формирую общую конфигурацию узла…',
        validating_config: 'Проверяю конфигурацию…', applying_config: 'Передаю конфигурацию Agent…',
        waiting_for_apply: 'Жду применения и healthcheck…', completed: 'Набор протоколов активирован.',
      },
    } as const;
  }
  return {
    title: 'Connection protocols',
    subtitle: 'One VPN account can use several protocols at the same time. Enabling another protocol does not disable existing working connections.',
    enabled: 'Enabled protocols', primary: 'Primary protocol',
    primaryHint: 'The primary protocol is the default where one connection method is required. Other enabled protocols remain available in parallel.',
    active: 'Active now', desired: 'Active after apply', auto: 'Auto — inherit node default',
    disabled: 'Not enabled', details: 'Details', nodeReady: 'The VPN node is ready for this protocol.',
    applyTitle: 'Primary protocol and activation',
    applySubtitle: 'Choose a primary protocol and apply the selected set.',
    vless: 'VLESS / Reality', wireguard: 'WireGuard', hysteria2: 'Hysteria2',
    shadowsocks: 'Shadowsocks 2022', mtproto: 'MTProto / FakeTLS',
    safety: 'Working connections are preserved until the new set is successfully applied.',
    save: 'Apply protocol set', retry: 'Retry apply', saving: 'Preparing...', saved: 'Protocol set applied successfully.',
    pending: 'The desired set is saved but not active yet. Previous working connections are preserved.',
    loading: 'Loading protocols...', loadError: 'Could not load protocol settings.', noServer: 'Assign a VPN node first.',
    saveError: 'The protocol set could not be applied. Previous active protocols should remain working.',
    selectOne: 'Select at least one protocol.', autoNeedsDefault: 'Auto requires the node-default protocol to be included in the enabled set.',
    setupRequired: 'Setup required',
    protocolsNotReady: 'Configure the selected protocols on the assigned VPN node first:',
    configureNode: 'Open node protocol settings',
    settingsLoading: 'Wait for the assigned VPN node settings to load.',
    settingsLoadError: 'Could not verify protocol readiness on the assigned VPN node.',
    hysteria2NotReady: 'Hysteria2 requires a dedicated TLS domain and an ACME email address.',
    errorDetail: 'Reason',
    awaitingApply: 'Client access for this account is not deployed on the node yet. Links and the subscription appear after a successful apply.',
    awaitingFirstApply: 'The node has no successfully applied configuration yet. Links and the subscription appear after the first successful apply.',
    applyAction: 'Press "Apply protocol set": the panel renders the node configuration and sends it to the Agent. You can also do this from the node configuration page.',
    savedSet: 'Saved set',
    servedSet: 'Actually active',
    unavailable: 'The client connection is currently unavailable.',
    stages: {
      saving_preference: 'Saving desired protocol set…', checking_runtime: 'Checking VPN runtimes…',
      installing_runtime: 'Installing required runtime…', rendering_config: 'Rendering combined node configuration…',
      validating_config: 'Validating configuration…', applying_config: 'Sending configuration to Agent…',
      waiting_for_apply: 'Waiting for apply and healthcheck…', completed: 'Protocol set activated.',
    },
  } as const;
}

function protocolLabel(protocol: string, copy: ReturnType<typeof getCopy>): string {
  switch (protocol) {
    case 'vless': return copy.vless;
    case 'wireguard': return copy.wireguard;
    case 'hysteria2': return copy.hysteria2;
    case 'shadowsocks': return copy.shadowsocks;
    case 'mtproto': return copy.mtproto;
    default: return protocol || '—';
  }
}

function protocolIsReady(protocol: ClientProtocol, settings: ProtocolSettingsResponse): boolean {
  switch (protocol) {
    case 'wireguard': return settings.wireGuard.ready;
    case 'hysteria2': return settings.hysteria2.ready;
    case 'shadowsocks': return settings.shadowsocks.ready;
    case 'mtproto': return settings.mtproto.ready;
    case 'vless':
    default:
      return settings.reality.enabled && settings.vless.port >= 1 && settings.vless.port <= 65535;
  }
}


function mutationErrorDetail(error: unknown, copy: ReturnType<typeof getCopy>): string {
  const detail = error instanceof Error ? error.message.trim() : typeof error === 'string' ? error.trim() : '';
  if (detail.includes('Hysteria2 TLS domain is required')) return copy.hysteria2NotReady;
  return detail;
}

export function VpnAccountProtocolPreferencePanel({ accountId, active, draft, onDraftChange }: Props) {
  const copy = getCopy();
  const queryClient = useQueryClient();
  // The profile state never carries client links, so it stays readable and
  // editable while client access is withheld until the node applies it.
  const queryKey = ['vpn-account-client-profile', accountId] as const;
  const mutationKey = ['vpn-account-protocol-apply', accountId] as const;
  const applying = useIsMutating({ mutationKey }) > 0;
  const [saved, setSaved] = useState(false);
  const [deploymentStage, setDeploymentStage] = useState<ProtocolDeploymentStage | null>(null);
  const [focusedProtocol, setFocusedProtocol] = useState<ClientProtocol>('vless');

  const stateQuery = useQuery({
    queryKey,
    queryFn: () => getVpnAccountClientProfileState(accountId),
    enabled: active,
  });
  const accountQuery = useQuery({ queryKey: ['vpn-account', accountId], queryFn: () => getVpnAccount(accountId) });
  const serversQuery = useQuery({ queryKey: ['servers'], queryFn: getServers });
  const assignedServerId = accountQuery.data?.serverId ?? '';
  const protocolSettingsQuery = useQuery({
    queryKey: ['server-protocol-settings', assignedServerId],
    queryFn: () => getProtocolSettings(assignedServerId),
    enabled: active && Boolean(assignedServerId),
  });

  const assignedServer = (serversQuery.data?.items ?? []).find((server) => server.id === assignedServerId);
  const nodeDefault = (protocolSettingsQuery.data?.protocol ?? 'vless') as ClientProtocol;

  const view = stateQuery.data ? protocolPreferenceView(stateQuery.data) : null;

  // Server refreshes update the saved/active summary, never an explicit draft.
  const primary = draft?.primary ?? stateQuery.data?.profile.protocol ?? 'auto';
  const enabledProtocols = draft?.enabledProtocols ?? view?.desired ?? ['vless'];

  const profile = stateQuery.data?.profile;
  const activeProtocols = view?.active ?? [];
  const storedDesired = view?.desired ?? [];
  const currentPrimary = profile?.protocol ?? 'auto';
  const changed = primary !== currentPrimary || !sameProtocols(enabledProtocols, storedDesired);
  const activationPending = view?.activationPending ?? false;
  const autoInvalid = primary === 'auto' && !enabledProtocols.includes(nodeDefault);
  const unreadyProtocols = protocolSettingsQuery.data
    ? enabledProtocols.filter((protocol) => !protocolIsReady(protocol, protocolSettingsQuery.data))
    : [];
  const validationMessage = enabledProtocols.length === 0
    ? copy.selectOne
    : autoInvalid
        ? copy.autoNeedsDefault
        : protocolSettingsQuery.isLoading
          ? copy.settingsLoading
          : protocolSettingsQuery.isError
            ? copy.settingsLoadError
            : unreadyProtocols.length > 0
              ? `${copy.protocolsNotReady} ${unreadyProtocols.map((protocol) => protocolLabel(protocol, copy)).join(' · ')}`
              : '';

  const saveMutation = useMutation({
    mutationKey,
    mutationFn: async () => {
      if (!active || stateQuery.isError || accountQuery.isError) throw new Error(copy.loadError);
      if (!profile) throw new Error(copy.loadError);
      if (!assignedServer) throw new Error(copy.noServer);
      if (validationMessage) throw new Error(validationMessage);

      const request: MultiProtocolUpdate = {
        name: profile.name,
        clientType: profile.clientType,
        deviceType: profile.deviceType,
        fingerprintMode: profile.fingerprintMode,
        fingerprint: profile.fingerprint,
        serverNameOverride: profile.serverNameOverride ?? '',
        spiderX: profile.spiderX || '/',
        mtu: profile.mtu ?? null,
        protocol: primary,
        enabledProtocols: ordered(enabledProtocols),
      };

      setDeploymentStage('saving_preference');
      // A retry must preserve the original change timestamp. Re-saving the
      // identical desired set makes an already rendered config version older
      // than the preference, preventing successful apply from activating it.
      if (changed) {
        await updateVpnAccountClientProfile(accountId, request);
      }

      for (const protocol of ordered(enabledProtocols)) {
        await ensureProtocolRuntime(assignedServer, protocol, setDeploymentStage);
      }
      await deployPendingProtocol(assignedServer.id, setDeploymentStage);

      const state = await getVpnAccountClientProfileState(accountId);
      if (!activationConfirmed(state, enabledProtocols)) {
        throw new Error('protocol_set_activation_not_confirmed');
      }
      return state;
    },
    onMutate: () => {
      setSaved(false);
      setDeploymentStage('saving_preference');
    },
    onSuccess: async (state) => {
      queryClient.setQueryData(queryKey, state);
      onDraftChange(null);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['vpn-account-client-connection', accountId] }),
        queryClient.invalidateQueries({ queryKey: ['vpn-account-routing-policy', accountId] }),
        queryClient.invalidateQueries({ queryKey: ['vpn-account', accountId] }),
        queryClient.invalidateQueries({ queryKey: ['servers'] }),
        queryClient.invalidateQueries({ queryKey: ['server-protocol-settings', assignedServerId] }),
        queryClient.invalidateQueries({ queryKey: ['server-config-versions', assignedServerId] }),
        queryClient.invalidateQueries({ queryKey: ['server-config-apply-jobs', assignedServerId] }),
      ]);
      setDeploymentStage('completed');
      setSaved(true);
    },
    onError: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey }),
        queryClient.invalidateQueries({ queryKey: ['vpn-account-client-connection', accountId] }),
      ]);
    },
  });

  const toggleProtocol = (protocol: ClientProtocol) => {
    if (applying) return;
    saveMutation.reset();
    setSaved(false);
    setDeploymentStage(null);
    const next = enabledProtocols.includes(protocol)
      ? enabledProtocols.filter((candidate) => candidate !== protocol)
      : [...enabledProtocols, protocol];
    const normalized = ordered(next);
    onDraftChange({
      primary: primary !== 'auto' && primary === protocol && !normalized.includes(protocol) && normalized.length > 0
        ? normalized[0] : primary,
      enabledProtocols: normalized,
    });
  };

  const stageText = deploymentStage ? copy.stages[deploymentStage] : null;
  const errorDetail = mutationErrorDetail(saveMutation.error, copy);
  const canRetry = !changed && activationPending && !view?.awaitingDeployment;
  const protocolList = (values: readonly ClientProtocol[]) =>
    values.length ? values.map((protocol) => protocolLabel(protocol, copy)).join(' · ') : '—';
  const awaitingText = view?.status === 'awaiting_first_apply' ? copy.awaitingFirstApply : copy.awaitingApply;
  const focusedReady = protocolSettingsQuery.data
    ? protocolIsReady(focusedProtocol, protocolSettingsQuery.data)
    : null;
  const focusedStatus = activeProtocols.includes(focusedProtocol)
    ? copy.active
    : enabledProtocols.includes(focusedProtocol) ? copy.desired : copy.disabled;

  return (
    <div className="vpn-account-protocol-workspace">
      <Section
        title={copy.title}
        description={copy.subtitle}
        aside={stateQuery.data && <span className="status-pill">{copy.active}: {protocolList(activeProtocols)}</span>}
      >
        {stateQuery.isLoading && <p className="empty-state">{copy.loading}</p>}
        {stateQuery.isError && <div className="form-message form-message-error">{copy.loadError}</div>}
        {stateQuery.data && (
          <div className="vpn-protocol-list-detail">
            <div className="vpn-protocol-list" role="group" aria-label={copy.enabled}>
              {protocolOrder.map((protocol) => {
                const selected = enabledProtocols.includes(protocol);
                const ready = protocolSettingsQuery.data
                  ? protocolIsReady(protocol, protocolSettingsQuery.data)
                  : true;
                const disabled = applying || stateQuery.isError || accountQuery.isError || (!selected && !ready);
                return (
                  <div
                    className={`vpn-protocol-row${focusedProtocol === protocol ? ' is-focused' : ''}`}
                    key={protocol}
                  >
                    <label className="vpn-protocol-row-label">
                      <input
                        type="checkbox"
                        checked={selected}
                        disabled={disabled}
                        onChange={() => { toggleProtocol(protocol); setFocusedProtocol(protocol); }}
                      />
                      <span className="vpn-protocol-choice-copy">
                        <strong>{protocolLabel(protocol, copy)}</strong>
                        <small>
                          {activeProtocols.includes(protocol) ? copy.active : selected ? copy.desired : copy.disabled}
                          {!ready && <> · <span className="vpn-protocol-setup-hint">{copy.setupRequired}</span></>}
                        </small>
                      </span>
                    </label>
                    <button
                      className="vpn-protocol-detail-trigger"
                      type="button"
                      aria-label={`${copy.details}: ${protocolLabel(protocol, copy)}`}
                      aria-pressed={focusedProtocol === protocol}
                      onClick={() => setFocusedProtocol(protocol)}
                    >
                      {copy.details}
                    </button>
                  </div>
                );
              })}
            </div>
            <div className="vpn-protocol-detail">
              <h4>{protocolLabel(focusedProtocol, copy)}</h4>
              <p>{focusedStatus}</p>
              <p>{protocolSettingsQuery.isLoading ? copy.settingsLoading
                : protocolSettingsQuery.isError ? copy.settingsLoadError
                  : focusedReady ? copy.nodeReady : copy.setupRequired}</p>
              {assignedServerId && (
                <Link className="text-link" to={`/protocol-settings/${encodeURIComponent(assignedServerId)}?protocol=${focusedProtocol}`}>
                  {copy.configureNode} →
                </Link>
              )}
            </div>
          </div>
        )}
      </Section>

      {stateQuery.data && (
        <Section title={copy.applyTitle} description={copy.applySubtitle}>
          <div className="vpn-account-protocol-preference-content">
            <div className="vpn-account-create-grid">
              <label className="field">
                <span>{copy.primary}</span>
                <select
                  value={primary}
                  disabled={applying || stateQuery.isError || accountQuery.isError}
                  onChange={(event) => {
                    onDraftChange({ primary: event.target.value as ClientProtocolPreference, enabledProtocols: ordered(enabledProtocols) });
                    saveMutation.reset();
                    setSaved(false);
                    setDeploymentStage(null);
                  }}
                >
                  <option value="auto">{copy.auto}</option>
                  {enabledProtocols.map((protocol) => (
                    <option key={protocol} value={protocol}>{protocolLabel(protocol, copy)}</option>
                  ))}
                </select>
                <span className="field-hint">{copy.primaryHint}</span>
              </label>
            </div>

            <div className="form-message">{copy.safety}</div>
            {view?.awaitingDeployment && !saveMutation.isPending && (
              <div className="form-message form-message-warning" role="status">
                <div>{awaitingText}</div>
                <div>{copy.savedSet}: {protocolList(view.desired)}</div>
                <div>{copy.servedSet}: {protocolList(view.active)}</div>
                <div>{copy.applyAction}</div>
                {view.message && <small>{copy.errorDetail}: {view.message}</small>}
              </div>
            )}
            {active && view?.activationPending && !changed && !applying
              && view.desired.includes('vless') && !view.active.includes('vless') && (
                <VpnAccountPreImportPanel key={accountId} accountId={accountId} />
              )}
            {!view?.awaitingDeployment && activationPending && !saveMutation.isPending && (
              <div className="form-message form-message-warning">
                <div>{copy.pending}</div>
                <div>{copy.savedSet}: {protocolList(storedDesired)}</div>
                <div>{copy.servedSet}: {protocolList(activeProtocols)}</div>
              </div>
            )}
            {(view?.status === 'unassigned' || (!assignedServer && accountQuery.isSuccess)) && (
              <div className="form-message form-message-warning">{copy.noServer}</div>
            )}
            {view?.status === 'unavailable' && (
              <div className="form-message form-message-warning">
                {copy.unavailable}{view.message && <div>{copy.errorDetail}: {view.message}</div>}
              </div>
            )}
            {validationMessage && (
              <div className="form-message form-message-warning vpn-protocol-readiness-warning">
                <span>{validationMessage}</span>
                {unreadyProtocols.includes('hysteria2') && <small>{copy.hysteria2NotReady}</small>}
                {assignedServerId && unreadyProtocols.length > 0 && (
                  <Link
                    className="small-button"
                    to={`/protocol-settings/${encodeURIComponent(assignedServerId)}?protocol=${encodeURIComponent(unreadyProtocols[0])}`}
                  >
                    {copy.configureNode}
                  </Link>
                )}
              </div>
            )}
            {changed && <div className="form-message">{copy.desired}: {enabledProtocols.map((protocol) => protocolLabel(protocol, copy)).join(' · ') || '—'}</div>}
            {saveMutation.isPending && stageText && <div className="form-message" role="status">{stageText}</div>}
            {saveMutation.isError && (isTransferReservationError(saveMutation.error) ?
              <TransferReservationNotice nodeIds={[assignedServerId]} blocked /> : (
              <div className="form-message form-message-error">
                {copy.saveError}{errorDetail && <div>{copy.errorDetail}: {errorDetail}</div>}
              </div>
            ))}
            {saved && <div className="form-message" role="status">{copy.saved}</div>}

            <div className="form-actions">
              <button
                className="primary-button"
                type="button"
                disabled={!active || !view || !canApplyProtocolSet(view, changed) || applying || stateQuery.isError || accountQuery.isError || Boolean(validationMessage) || !assignedServer}
                onClick={() => { if (!applying && active) saveMutation.mutate(); }}
              >
                {saveMutation.isPending ? stageText ?? copy.saving : canRetry ? copy.retry : copy.save}
              </button>
            </div>
          </div>
        </Section>
      )}
    </div>
  );
}
