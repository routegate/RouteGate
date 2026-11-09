import { apiDelete, apiGet, apiPatch, apiPost, apiPut } from '../../../shared/api/client';

export interface VpnAccount {
  id: string;
  displayName: string;
  email?: string | null;
  phone?: string | null;
  telegramUsername?: string | null;
  telegramRecipientId?: string | null;
  status: string;
  expiresAt?: string | null;
  maxDevices?: number | null;
  serverId?: string | null;
  vlessUuid?: string | null;
  createdAt: string;
  updatedAt: string;
  configUpdatedAt?: string;
}

export type ClientProtocol = 'vless' | 'wireguard' | 'hysteria2' | 'shadowsocks' | 'mtproto';
export type ClientProtocolPreference = 'auto' | ClientProtocol;
export type RoutingProfileSource = 'none' | 'account' | 'server' | 'default';

export interface RoutingProfilePolicySummary {
  id: string;
  name: string;
  description?: string;
  isDefault: boolean;
}

export interface NodeGroupPolicySummary {
  id: string;
  name: string;
  selectionStrategy: 'priority' | 'weighted';
  memberCount: number;
}

export interface VpnAccountRoutingPolicy {
  vpnAccountId: string;
  explicitRoutingProfile?: RoutingProfilePolicySummary;
  effectiveRoutingProfile?: RoutingProfilePolicySummary;
  routingProfileSource: RoutingProfileSource;
  nodeGroup?: NodeGroupPolicySummary;
  currentServerInGroup: boolean;
  automaticSelection: boolean;
  automaticSelectionPolicy: AutomaticSelectionPolicy;
  protocol?: string;
  clientRoutingSupported: boolean;
}

export interface AutomaticSelectionPolicy {
  enabled: boolean;
  allowDegraded: boolean;
  cooldownSeconds: number;
  lastSelectedAt?: string;
  lastSelectedServerId?: string;
}

export type AutomaticSelectionStatus =
  | 'selected'
  | 'current'
  | 'no_eligible_candidates'
  | 'node_group_required'
  | 'cooldown';

export interface AutomaticSelectionDecision {
  vpnAccountId: string;
  nodeGroupId?: string;
  selectionStrategy?: 'priority' | 'weighted';
  status: AutomaticSelectionStatus;
  currentServerId?: string;
  selectedCandidate?: {
    serverId: string;
    serverName: string;
    protocol: string;
    health: 'ready' | 'degraded';
    priority: number;
    weight: number;
    signals: string[];
  };
  reasons: string[];
  eligibleCandidates: number;
  evaluatedAt: string;
  blockedUntil?: string;
  canApply: boolean;
}

export interface AutomaticSelectionApplyResponse {
  decision: AutomaticSelectionDecision;
  previousServerId?: string;
  selectedServerId: string;
  changed: boolean;
  affectedServerIds: string[];
  configDeploymentRequired: boolean;
}

export interface ListVpnAccountsResponse {
  items: VpnAccount[];
}

export interface CreateVpnAccountRequest {
  displayName: string;
  email?: string;
  phone?: string;
  telegramUsername?: string;
  serverId?: string;
}

export interface VpnAccountCredentialsResponse {
  vpnAccountId: string;
  serverId?: string;
  endpoint?: string;
  protocol: ClientProtocol;
  vless: {
    uuid: string;
    flow?: string;
    network?: string;
  };
  reality: {
    enabled: boolean;
    publicKey?: string;
    shortId?: string;
    serverName?: string;
  };
  wireGuard: {
    privateKey?: string;
    publicKey?: string;
    address?: string;
    serverPublicKey?: string;
    dns?: string;
  };
  hysteria2: {
    username?: string;
    password?: string;
    domain?: string;
    port?: number;
    acmeEmail?: string;
  };
  shadowsocks: {
    username?: string;
    method?: string;
    serverKey?: string;
    userKey?: string;
    port?: number;
  };
  mtproto: {
    secret?: string;
    port?: number;
    frontingDomain?: string;
    shared: boolean;
  };
}

export interface TrafficUsageSummaryResponse {
  vpnAccountId: string;
  period: {
    from: string;
    to: string;
  };
  usage: {
    rxBytes: number;
    txBytes: number;
    totalBytes: number;
  };
  limit?: {
    monthlyLimitBytes?: number | null;
    hardLimitEnabled: boolean;
    speedLimitBps?: number | null;
    resetDay: number;
    usedPercent?: number | null;
    remainingBytes?: number | null;
    limitReached: boolean;
    enforced: boolean;
    limitExceededAt?: string | null;
    enforcementStatus: string;
    enforcementUpdatedAt?: string | null;
    updatedAt: string;
  } | null;
}

export interface UpdateTrafficLimitRequest {
  monthlyLimitBytes?: number | null;
  hardLimitEnabled: boolean;
  speedLimitBps?: number | null;
  resetDay?: number | null;
}

export interface TrafficLimitResponse {
  vpnAccountId: string;
  monthlyLimitBytes?: number | null;
  hardLimitEnabled: boolean;
  speedLimitBps?: number | null;
  resetDay: number;
  limitExceededAt?: string | null;
  enforcementStatus: string;
  enforcementUpdatedAt?: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface SubscriptionTokenResponse {
  vpnAccountId: string;
  subscriptionToken: string;
  subscriptionUrl: string;
  expiresAt?: string | null;
}

// LegacySubscriptionAccess is the safe, non-secret read model for the
// account-level ("legacy") subscription token that predates RG-116 Access &
// Devices - it never carries the token hash or plaintext, only whether an
// active legacy credential currently exists and, if so, when it was
// issued/rotated, when it expires, and when it was last used.
export interface LegacySubscriptionAccess {
  vpnAccountId: string;
  hasActiveToken: boolean;
  createdAt?: string | null;
  expiresAt?: string | null;
  lastUsedAt?: string | null;
}

export interface SubscriptionQRCodeResponse {
  vpnAccountId: string;
  subscriptionUrl: string;
  qrText: string;
  format: string;
}

export type ClientFingerprintMode = 'auto' | 'manual';

export interface VpnClientProfile {
  id: string;
  vpnAccountId: string;
  name: string;
  clientType: string;
  deviceType: string;
  fingerprintMode: ClientFingerprintMode;
  fingerprint: string;
  resolvedFingerprint: string;
  serverNameOverride?: string;
  spiderX: string;
  mtu?: number | null;
  protocol: ClientProtocolPreference;
  createdAt: string;
  updatedAt: string;
}

export interface VpnClientConnectionResponse {
  vpnAccountId: string;
  protocol: ClientProtocol;
  format: string;
  vlessLink?: string;
  wireGuardConfig?: string;
  hysteria2Uri?: string;
  shadowsocksUri?: string;
  mtprotoUri?: string;
  profile: VpnClientProfile;
  endpoint: string;
  serverName: string;
  network: string;
  flow?: string;
}

export interface UpdateVpnClientProfileRequest {
  name: string;
  clientType: string;
  deviceType: string;
  fingerprintMode: ClientFingerprintMode;
  fingerprint: string;
  serverNameOverride: string;
  spiderX: string;
  mtu?: number | null;
  protocol: ClientProtocolPreference;
}

interface SingBoxClientReality {
  enabled?: boolean;
  public_key?: string;
  short_id?: string;
}

interface SingBoxClientTLS {
  enabled?: boolean;
  server_name?: string;
  reality?: SingBoxClientReality;
}

interface SingBoxClientOutbound {
  type?: string;
  server?: string;
  server_port?: number;
  uuid?: string;
  flow?: string;
  network?: string;
  tls?: SingBoxClientTLS;
}

interface SingBoxClientConfig {
  outbounds?: SingBoxClientOutbound[];
  [key: string]: unknown;
}

export interface PublicSubscriptionResponse {
  status: string;
  format: string;
  generatedAt: string;
  vpnAccountId: string;
  account: {
    id: string;
    displayName: string;
    status: string;
    expiresAt?: string | null;
    maxDevices?: number | null;
  };
  server?: {
    id: string;
    name: string;
    hostname?: string;
    publicIp?: string;
    endpoint?: string;
    location?: string;
    provider?: string;
  } | null;
  config: {
    type: string;
    format: string;
    status: string;
    message?: string;
    rendered?: {
      format: string;
      content?: SingBoxClientConfig;
      text?: string;
    } | null;
  };
}

function requiredText(value: string | undefined, field: string): string {
  const normalized = value?.trim() ?? '';
  if (normalized === '') {
    throw new Error(`${field} is required to create a VLESS connection link.`);
  }
  return normalized;
}

function formatVlessHost(server: string): string {
  if (server.includes(':') && !server.startsWith('[')) {
    return `[${server}]`;
  }
  return server;
}

export function buildVlessRealityShareLink(
  subscription: PublicSubscriptionResponse,
  fingerprint = 'firefox',
): string {
  const content = subscription.config.rendered?.content;
  const outbound = content?.outbounds?.find(
    (candidate) => candidate.type?.trim().toLowerCase() === 'vless',
  );
  if (!outbound) {
    throw new Error('Rendered client config does not contain a VLESS outbound.');
  }

  const server = requiredText(outbound.server, 'VLESS server');
  const uuid = requiredText(outbound.uuid, 'VLESS UUID');
  const serverName = requiredText(outbound.tls?.server_name, 'Reality server name');
  const publicKey = requiredText(outbound.tls?.reality?.public_key, 'Reality public key');
  const serverPort = outbound.server_port;
  if (typeof serverPort !== 'number' || !Number.isInteger(serverPort) || serverPort < 1 || serverPort > 65535) {
    throw new Error('VLESS server port is invalid.');
  }
  if (!outbound.tls?.enabled || !outbound.tls.reality?.enabled) {
    throw new Error('Rendered client config does not enable Reality.');
  }

  const parameters = new URLSearchParams();
  parameters.set('encryption', 'none');
  parameters.set('security', 'reality');
  parameters.set('type', outbound.network?.trim() || 'tcp');
  parameters.set('sni', serverName);
  parameters.set('fp', fingerprint);
  parameters.set('pbk', publicKey);
  if (typeof outbound.tls.reality.short_id === 'string') {
    parameters.set('sid', outbound.tls.reality.short_id.trim());
  }
  if (outbound.flow?.trim()) {
    parameters.set('flow', outbound.flow.trim());
  }

  const label = subscription.account.displayName.trim()
    || subscription.server?.name?.trim()
    || 'RouteGate';

  return `vless://${encodeURIComponent(uuid)}@${formatVlessHost(server)}:${serverPort}?${parameters.toString()}#${encodeURIComponent(label)}`;
}

export type NodeClientAccessState = 'served' | 'not_served' | 'unknown';

/**
 * Whether a node issues client access to at least one active account, from
 * the Manager's own client-link evaluation. Carries account ids only.
 * 'unknown' means the evaluation could not finish, not that access is missing.
 */
export interface NodeClientAccess {
  serverId: string;
  activeAccounts: number;
  state: NodeClientAccessState;
  servedAccountId?: string;
  pendingAccountId?: string;
  pendingStatus?: VpnClientConnectionStatus;
  pendingMessage?: string;
}

export function getVpnAccessSummary(): Promise<{ items: NodeClientAccess[] }> {
  return apiGet<{ items: NodeClientAccess[] }>('/api/v1/vpn-accounts/access-summary');
}

export async function createVpnAccount(request: CreateVpnAccountRequest): Promise<VpnAccount> {
  const account = await apiPost<CreateVpnAccountRequest, VpnAccount>('/api/v1/vpn-accounts', request);
  return activateVpnAccount(account.id);
}

export function activateVpnAccount(vpnAccountId: string): Promise<VpnAccount> {
  return apiPost<undefined, VpnAccount>(
    `/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/activate`,
  );
}

export function getVpnAccountCredentials(
  vpnAccountId: string,
): Promise<VpnAccountCredentialsResponse> {
  return apiGet<VpnAccountCredentialsResponse>(
    `/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/credentials`,
  );
}

export function getV2rayNRoutingRules(vpnAccountId: string): Promise<unknown[]> {
  return apiGet<unknown[]>(`/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/routing-policy/v2rayn`);
}

export function getVpnAccountRoutingPolicy(vpnAccountId: string): Promise<VpnAccountRoutingPolicy> {
  return apiGet<VpnAccountRoutingPolicy>(
    `/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/routing-policy`,
  );
}

export function assignVpnAccountRoutingProfile(
  vpnAccountId: string,
  routingProfileId: string,
): Promise<VpnAccountRoutingPolicy> {
  return apiPut<{ routingProfileId: string }, VpnAccountRoutingPolicy>(
    `/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/routing-profile`,
    { routingProfileId },
  );
}

export function clearVpnAccountRoutingProfile(vpnAccountId: string): Promise<VpnAccountRoutingPolicy> {
  return apiDelete<VpnAccountRoutingPolicy>(
    `/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/routing-profile`,
  );
}

export function assignVpnAccountNodeGroup(
  vpnAccountId: string,
  nodeGroupId: string,
): Promise<VpnAccountRoutingPolicy> {
  return apiPut<{ nodeGroupId: string }, VpnAccountRoutingPolicy>(
    `/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/node-group`,
    { nodeGroupId },
  );
}

export function clearVpnAccountNodeGroup(vpnAccountId: string): Promise<VpnAccountRoutingPolicy> {
  return apiDelete<VpnAccountRoutingPolicy>(
    `/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/node-group`,
  );
}

export function updateVpnAccountAutomaticSelection(
  vpnAccountId: string,
  policy: Pick<AutomaticSelectionPolicy, 'enabled' | 'allowDegraded' | 'cooldownSeconds'>,
): Promise<VpnAccountRoutingPolicy> {
  return apiPut<typeof policy, VpnAccountRoutingPolicy>(
    `/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/automatic-selection`,
    policy,
  );
}

export function previewVpnAccountAutomaticSelection(
  vpnAccountId: string,
): Promise<AutomaticSelectionDecision> {
  return apiGet<AutomaticSelectionDecision>(
    `/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/automatic-selection/preview`,
  );
}

export function applyVpnAccountAutomaticSelection(
  vpnAccountId: string,
): Promise<AutomaticSelectionApplyResponse> {
  return apiPost<undefined, AutomaticSelectionApplyResponse>(
    `/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/automatic-selection/apply`,
  );
}

export function getVpnAccountClientConnection(
  vpnAccountId: string,
): Promise<VpnClientConnectionResponse> {
  return apiGet<VpnClientConnectionResponse>(
    `/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/client-connection`,
  );
}

export interface UnappliedVLESSPreview {
  status: 'unapplied_preview';
  protocol: 'vless';
  format: 'vless-reality-uri';
  vlessUri: string;
  warning: string;
}

/** Explicit operator POST; no subscription bearer is issued or stored. */
export function previewUnappliedVLESS(
  accountId: string,
  request: { acknowledgeUnapplied: true },
): Promise<UnappliedVLESSPreview> {
  return apiPost<typeof request, UnappliedVLESSPreview>(
    `/api/v1/vpn-accounts/${encodeURIComponent(accountId)}/client-connection/pre-import`,
    request,
  );
}

export type VpnClientConnectionStatus =
  | 'ready'
  | 'awaiting_apply'
  | 'awaiting_first_apply'
  | 'unassigned'
  | 'unavailable';

/**
 * Saved protocol preferences plus the node's real serving state. It never
 * carries client links, so it can be shown while access awaits an apply.
 */
export interface VpnClientProfileStateResponse {
  vpnAccountId: string;
  /** enabledProtocols is the saved desired set; activeProtocols what the node serves now. */
  profile: VpnClientProfile & { enabledProtocols?: ClientProtocol[]; activeProtocols?: ClientProtocol[] };
  activeProtocol: ClientProtocol;
  connectionStatus: VpnClientConnectionStatus;
  connectionMessage?: string;
}

export function getVpnAccountClientProfileState(
  vpnAccountId: string,
): Promise<VpnClientProfileStateResponse> {
  return apiGet<VpnClientProfileStateResponse>(
    `/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/client-profile`,
  );
}

export function updateVpnAccountClientProfile(
  vpnAccountId: string,
  request: UpdateVpnClientProfileRequest,
): Promise<VpnClientConnectionResponse> {
  return apiPatch<UpdateVpnClientProfileRequest, VpnClientConnectionResponse>(
    `/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/client-profile`,
    request,
  );
}

export function getVpnAccountTraffic(
  vpnAccountId: string,
): Promise<TrafficUsageSummaryResponse> {
  return apiGet<TrafficUsageSummaryResponse>(
    `/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/traffic`,
  );
}

export function updateVpnAccountTrafficLimit(
  vpnAccountId: string,
  request: UpdateTrafficLimitRequest,
): Promise<TrafficLimitResponse> {
  return apiPatch<UpdateTrafficLimitRequest, TrafficLimitResponse>(
    `/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/traffic-limit`,
    request,
  );
}

export function createVpnAccountSubscriptionToken(
  vpnAccountId: string,
): Promise<SubscriptionTokenResponse> {
  return apiPost<undefined, SubscriptionTokenResponse>(
    `/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/subscription-token`,
  );
}

export function rotateVpnAccountSubscriptionToken(
  vpnAccountId: string,
): Promise<SubscriptionTokenResponse> {
  return apiPost<undefined, SubscriptionTokenResponse>(
    `/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/subscription-token/rotate`,
  );
}

export function getVpnAccountLegacySubscriptionAccess(
  vpnAccountId: string,
): Promise<LegacySubscriptionAccess> {
  return apiGet<LegacySubscriptionAccess>(
    `/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/subscription-token`,
  );
}

export function revokeVpnAccountSubscriptionToken(vpnAccountId: string): Promise<void> {
  return apiDelete<void>(
    `/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/subscription-token`,
  );
}

export function getVpnAccountSubscriptionQRCode(
  vpnAccountId: string,
  subscriptionToken: string,
): Promise<SubscriptionQRCodeResponse> {
  return apiGet<SubscriptionQRCodeResponse>(
    `/api/v1/vpn-accounts/${encodeURIComponent(vpnAccountId)}/qr?token=${encodeURIComponent(subscriptionToken)}`,
  );
}

export function getPublicSubscription(
  subscriptionToken: string,
): Promise<PublicSubscriptionResponse> {
  return apiGet<PublicSubscriptionResponse>(
    `/api/v1/subscriptions/${encodeURIComponent(subscriptionToken)}`,
  );
}
