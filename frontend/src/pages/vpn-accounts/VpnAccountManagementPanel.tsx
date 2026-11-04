import { type FormEvent, useEffect, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import {
  activateVpnAccountManagement,
  deleteVpnAccount,
  getVpnAccount,
  getVpnAccountNotes,
  revokeVpnAccount,
  suspendVpnAccount,
  updateVpnAccount,
  updateVpnAccountNotes,
} from '../../entities/vpnAccount/api/vpnAccountManagementApi';
import { t } from '../../shared/i18n/i18n';
import { EmptyState } from '../../shared/ui/EmptyState';
import { Section } from '../../shared/ui/Section';
import { Notice } from '../../shared/ui/Notice';
import { StatusBadge } from '../../shared/ui/StatusBadge';
import { getVpnAccountManagementCopy } from './vpnAccountManagementCopy';
import { getAccountTransfer } from '../../entities/vpnAccount/api/vpnAccountTransferApi';

// Identity/administrative data only. Status changes go through the explicit
// Activate/Suspend/Revoke actions below (never a status dropdown), and server
// placement lives in Routing & Placement, not here.
export function VpnAccountManagementPanel({ accountId }: { accountId?: string }) {
  const copy = getVpnAccountManagementCopy();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const queryClient = useQueryClient();
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const [identityDirty, setIdentityDirty] = useState(false);
  const [notesDirty, setNotesDirty] = useState(false);

  const [displayName, setDisplayName] = useState('');
  const [email, setEmail] = useState('');
  const [phone, setPhone] = useState('');
  const [telegramUsername, setTelegramUsername] = useState('');
  const [notes, setNotes] = useState('');
  const [message, setMessage] = useState('');
  const [errorMessage, setErrorMessage] = useState('');

  const accountQuery = useQuery({
    queryKey: ['vpn-account', accountId],
    queryFn: () => getVpnAccount(accountId ?? ''),
    enabled: Boolean(accountId),
  });

  const notesQuery = useQuery({
    queryKey: ['vpn-account-notes', accountId],
    queryFn: () => getVpnAccountNotes(accountId ?? ''),
    enabled: Boolean(accountId),
  });
  // Deletion is never a way to shortcut a reserved RG-140 transfer.
  // Fail closed until the read-only transfer status can be verified.
  const transferQuery = useQuery({
    queryKey: ['vpn-account-transfer', accountId],
    queryFn: () => getAccountTransfer(accountId ?? ''),
    enabled: Boolean(accountId),
    refetchInterval: 15000,
  });
  const pendingTransfer = Boolean(transferQuery.data?.transfer && !transferQuery.data.transfer.completedAt);
  const deletionBlocked = transferQuery.isPending || transferQuery.isError || pendingTransfer;

  useEffect(() => {
    const account = accountQuery.data;
    if (!account || identityDirty) return;
    setDisplayName(account.displayName);
    setEmail(account.email ?? '');
    setPhone(account.phone ?? '');
    setTelegramUsername(account.telegramUsername ?? '');
  }, [
    identityDirty,
    accountQuery.data?.id,
    accountQuery.data?.displayName,
    accountQuery.data?.email,
    accountQuery.data?.phone,
    accountQuery.data?.telegramUsername,
  ]);

  useEffect(() => {
    if (!notesQuery.data || notesDirty) return;
    setNotes(notesQuery.data.notes ?? '');
  }, [notesQuery.data?.notes, notesDirty]);

  async function refreshAccountData() {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['vpn-accounts'] }),
      queryClient.invalidateQueries({ queryKey: ['vpn-account', accountId] }),
      queryClient.invalidateQueries({ queryKey: ['vpn-account-notes', accountId] }),
    ]);
  }

  const updateMutation = useMutation({
    mutationFn: async () => {
      const previous = accountQuery.data;
      if (!previous) throw new Error('VPN account is not loaded');

      const nextDisplayName = displayName.trim();
      const nextEmail = email.trim();
      const nextPhone = phone.trim();
      const nextTelegramUsername = telegramUsername.trim();
      const previousNotes = notesQuery.data?.notes ?? '';

      const accountChanged = previous.displayName !== nextDisplayName
        || (previous.email ?? '') !== nextEmail
        || (previous.phone ?? '') !== nextPhone
        || (previous.telegramUsername ?? '') !== nextTelegramUsername;
      const notesChanged = previousNotes !== notes;

      const accountPromise = accountChanged
        ? updateVpnAccount(accountId ?? '', {
            displayName: nextDisplayName,
            email: nextEmail,
            phone: nextPhone,
            telegramUsername: nextTelegramUsername,
          })
        : Promise.resolve(previous);
      const notesPromise = notesChanged
        ? updateVpnAccountNotes(accountId ?? '', notes)
        : Promise.resolve(notesQuery.data);

      await Promise.all([accountPromise, notesPromise]);
    },
    onSuccess: async () => {
      setMessage(copy.editSuccess);
      setErrorMessage('');
      await refreshAccountData();
      setIdentityDirty(false);
      setNotesDirty(false);
    },
    onError: () => {
      setMessage('');
      setErrorMessage(copy.editError);
    },
  });

  const statusMutation = useMutation({
    mutationFn: (nextStatus: 'active' | 'suspended' | 'revoked') => {
      if (!accountId) throw new Error('Missing VPN account ID');
      if (nextStatus === 'active') return activateVpnAccountManagement(accountId);
      if (nextStatus === 'suspended') return suspendVpnAccount(accountId);
      return revokeVpnAccount(accountId);
    },
    onSuccess: async (_result, nextStatus) => {
      setMessage(nextStatus === 'active' ? copy.statusRecordedActive : copy.statusRecordedPending);
      setErrorMessage('');
      await refreshAccountData();
    },
    onError: () => {
      setMessage('');
      setErrorMessage(copy.editError);
    },
  });

  const deleteMutation = useMutation({
    mutationFn: () => deleteVpnAccount(accountId ?? ''),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['vpn-accounts'] });
      if (!mounted.current) return;
      const query = searchParams.toString();
      navigate(`/vpn-accounts${query ? `?${query}` : ''}`, { replace: true });
    },
    onError: () => {
      setErrorMessage(copy.deleteError);
      void transferQuery.refetch();
    },
  });

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!displayName.trim() || updateMutation.isPending || statusMutation.isPending || deleteMutation.isPending || !notesQuery.isSuccess) return;
    updateMutation.mutate();
  }

  function handleDelete() {
    if (deletionBlocked) return;
    const name = accountQuery.data?.displayName ?? accountId ?? '';
    if (window.confirm(copy.deleteConfirm(name))) deleteMutation.mutate();
  }

  function handleRevoke() {
    const name = accountQuery.data?.displayName ?? accountId ?? '';
    if (window.confirm(copy.revokeConfirm(name))) statusMutation.mutate('revoked');
  }

  function handleSuspend() {
    const name = accountQuery.data?.displayName ?? accountId ?? '';
    if (window.confirm(copy.suspendConfirm(name))) statusMutation.mutate('suspended');
  }

  function handleActivate() {
    const name = accountQuery.data?.displayName ?? accountId ?? '';
    if (window.confirm(copy.activateConfirm(name))) statusMutation.mutate('active');
  }

  if (!accountId) {
    return (
      <div className="panel feature-detail-panel vpn-account-management-panel">
        <EmptyState title={t('vpnAccounts.selectTitle')} description={t('vpnAccounts.selectDescription')} />
      </div>
    );
  }

  if (accountQuery.isLoading) {
    return <div className="panel feature-detail-panel vpn-account-management-panel"><p className="empty-state">{t('common.loading')}</p></div>;
  }

  if (accountQuery.isError || !accountQuery.data) {
    return <div className="panel feature-detail-panel vpn-account-management-panel"><Notice tone="error">{copy.loadAccountError}</Notice></div>;
  }

  const account = accountQuery.data;
  const actionPending = statusMutation.isPending || deleteMutation.isPending || updateMutation.isPending;

  return (
    <div className="vpn-account-settings">
      {message && <Notice tone="success" announcement="polite">{message}</Notice>}
      {errorMessage && <Notice tone="error" announcement="assertive">{errorMessage}</Notice>}

      <Section title={copy.identityTitle} description={copy.identitySubtitle}>
        <form className="vpn-account-edit-form" onSubmit={handleSubmit}>
          <div className="vpn-account-edit-grid" onChange={(event) => {
            if (event.target instanceof HTMLTextAreaElement) setNotesDirty(true);
            else setIdentityDirty(true);
          }}>
            <label className="field">
              <span>{copy.accountName}</span>
              <input disabled={actionPending} value={displayName} onChange={(event) => setDisplayName(event.target.value)} required />
              <small>{copy.accountNameHint}</small>
            </label>
            <label className="field">
              <span>{t('vpnAccounts.email')}</span>
              <input disabled={actionPending} type="email" value={email} onChange={(event) => setEmail(event.target.value)} />
            </label>
            <label className="field">
              <span>{t('vpnAccounts.phone')}</span>
              <input
                type="tel"
                disabled={actionPending}
                value={phone}
                placeholder={t('vpnAccounts.phonePlaceholder')}
                onChange={(event) => setPhone(event.target.value)}
              />
              <small>{t('vpnAccounts.phoneHint')}</small>
            </label>
            <label className="field">
              <span>{t('vpnAccounts.telegramUsername')}</span>
              <input
                value={telegramUsername}
                disabled={actionPending}
                placeholder={t('vpnAccounts.telegramUsernamePlaceholder')}
                onChange={(event) => setTelegramUsername(event.target.value)}
              />
              <small>{t('vpnAccounts.telegramUsernameHint')}</small>
            </label>
            <label className="field vpn-account-notes-field">
              <span>{copy.notes}</span>
              <textarea
                value={notes}
                disabled={actionPending || !notesQuery.isSuccess}
                maxLength={4000}
                rows={4}
                placeholder={copy.notesPlaceholder}
                onChange={(event) => setNotes(event.target.value)}
              />
              <small>{copy.notesHint}</small>
            </label>
          </div>

          {notesQuery.isError && <Notice tone="error">{copy.notesLoadError}</Notice>}

          <div className="form-actions">
            <button className="primary-button" type="submit" disabled={!displayName.trim() || actionPending || !notesQuery.isSuccess}>
              {updateMutation.isPending ? copy.saving : copy.save}
            </button>
          </div>
        </form>
      </Section>

      <Section title={copy.lifecycleTitle} description={copy.lifecycleSubtitle} aside={<StatusBadge status={account.status} />}>
        {['suspended', 'revoked', 'expired'].includes(account.status) && (
          <Notice tone="warning">
            <p>{copy.runtimeNotConfirmed}</p>
            {account.serverId && (
              <Link to={`/vpn-accounts/${encodeURIComponent(account.id)}/protocols`}>
                {copy.verifyNodeAccess}
              </Link>
            )}
          </Notice>
        )}
        <div className="vpn-account-lifecycle-actions">
          <button className="small-button" type="button" disabled={actionPending || account.status === 'active'} onClick={handleActivate}>{copy.activate}</button>
          <button className="small-button" type="button" disabled={actionPending || account.status === 'suspended'} onClick={handleSuspend}>{copy.suspend}</button>
          <button className="small-button" type="button" disabled={actionPending || account.status === 'revoked'} onClick={handleRevoke}>{copy.revoke}</button>
        </div>
      </Section>

      <Section title={copy.dangerZone} description={copy.dangerSubtitle} tone="danger">
        {pendingTransfer && (
          <Notice tone="warning">
            <p>{copy.transferDeleteBlocked}</p>
            <Link to={`/vpn-accounts/${encodeURIComponent(account.id)}/routing`}>
              {copy.transferOpenRouting}
            </Link>
          </Notice>
        )}
        {transferQuery.isPending && <Notice tone="warning">{copy.transferCheckPending}</Notice>}
        {transferQuery.isError && <Notice tone="error">{copy.transferCheckFailed}</Notice>}
        <div className="vpn-account-delete-action">
          <span className="vpn-account-delete-id">{account.id}</span>
          <button className="danger-button" type="button" disabled={actionPending || deletionBlocked} onClick={handleDelete}>
            {copy.deleteAccount}
          </button>
        </div>
      </Section>
    </div>
  );
}
