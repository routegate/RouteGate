import { transferReservationsKey } from '../../entities/vpnAccount/api/vpnAccountTransferApi';
import { useState } from 'react';
import { accountTransferError, accountTransferErrorIdentity } from './accountTransferMessages';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { actAccountTransfer, type AccountTransfer, type TransferState } from '../../entities/vpnAccount/api/vpnAccountTransferApi';
import { getCurrentLocale } from '../../shared/i18n/i18n';

export function AccountTransferPanel({ accountId, transfer, serverNames }: { accountId: string; transfer: AccountTransfer; serverNames: Record<string, string> }) {
  const qc = useQueryClient();
  const ru = getCurrentLocale() === 'ru';
  const [confirmed, setConfirmed] = useState(false);
  const labels: Record<TransferState, string> = ru ? {
    preparing: 'Подготовка нового узла', target_applying: 'Конфигурация нового узла применяется', target_ready: 'Новый узел проверен',
    client_refresh_pending: 'Подписка переключена — проверьте клиентов', source_cleaning: 'Удаление старого доступа', target_cleaning: 'Удаление подготовленного доступа после отмены',
    complete: 'Перенос завершён', cancelled: 'Подготовка отменена', rolled_back: 'Исходный узел восстановлен',
  } : {
    preparing: 'Preparing target', target_applying: 'Applying target configuration', target_ready: 'Target verified',
    client_refresh_pending: 'Subscription switched — verify clients', source_cleaning: 'Removing source access', target_cleaning: 'Removing staged access after cancellation',
    complete: 'Transfer complete', cancelled: 'Preparation cancelled', rolled_back: 'Source restored',
  };
  const mutation = useMutation({
    mutationFn: (action: string) => actAccountTransfer(accountId, transfer.id, action, confirmed),
    onSuccess: async () => {
      setConfirmed(false);
      await Promise.all([
        qc.invalidateQueries({ queryKey: ['vpn-account-transfer', accountId] }),
        qc.invalidateQueries({ queryKey: transferReservationsKey }),
        qc.invalidateQueries({ queryKey: ['vpn-account', accountId] }),
        qc.invalidateQueries({ queryKey: ['vpn-account-routing-policy', accountId] }),
        qc.invalidateQueries({ queryKey: ['vpn-account-client-connection', accountId] }),
        qc.invalidateQueries({ queryKey: ['vpn-accounts'] }),
      ]);
    },
    onError: () => qc.invalidateQueries({ queryKey: ['vpn-account-transfer', accountId] }),
  });
  const duplicateError = Boolean(transfer.lastError) && mutation.isError
    && accountTransferErrorIdentity(transfer.lastError) === accountTransferErrorIdentity(mutation.error);
  const state = transfer.state;
  const terminal = Boolean(transfer.completedAt);
  const next = state === 'target_applying' ? 'verify' : state === 'target_ready' ? 'cutover'
    : state === 'client_refresh_pending' ? 'cleanup' : state === 'source_cleaning' || state === 'target_cleaning' ? 'finish' : null;
  const actions: Record<string, string> = ru ? {
    verify: 'Проверить новый узел', cutover: 'Переключить подписку', cleanup: 'Удалить доступ со старого узла', finish: 'Проверить завершение', retry: 'Повторить неудачное применение', rollback: 'Восстановить исходный узел и очистить новый',
  } : {
    verify: 'Verify target', cutover: 'Switch subscription', cleanup: 'Remove source access', finish: 'Verify completion', retry: 'Retry failed apply', rollback: 'Restore source and clean target',
  };
  const stalled = !terminal && Date.now() - Date.parse(transfer.updatedAt) > 24 * 3600 * 1000;
  return <div className="vpn-account-routing-form account-transfer-panel" aria-live="polite">
    <strong>{labels[state]}</strong>
    <p>{serverNames[transfer.sourceServerId] ?? transfer.sourceServerId} → {serverNames[transfer.targetServerId] ?? transfer.targetServerId}</p>
    <p>{ru ? 'Ссылка сохраняется. До переключения работает исходная подписка; после переключения старый узел сохраняет доступ до проверки клиентов.' : 'The link stays unchanged. The source subscription remains active until cutover; source access remains available until clients are verified.'}</p>
    {stalled && <div className="form-message form-message-warning">{ru ? 'Операция ожидает действий больше суток. Узлы остаются зарезервированы; завершите проверку или восстановите исходный узел. Автоматического удаления доступа нет.' : 'This operation has been waiting for over a day. Nodes remain reserved; complete verification or restore the source. Access is never removed by a timer.'}</div>}
    {state === 'client_refresh_pending' && <>
      <p>{ru ? 'В Hiddify, v2rayN или v2rayNG обновите существующую подписку, подключитесь и проверьте выходной IP. Запрос подписки показывает только обращение клиента, а не подключение к новому узлу.' : 'In Hiddify, v2rayN or v2rayNG, refresh the existing subscription, connect and verify the exit IP. A subscription request proves retrieval only, not use of the new node.'}</p>
      <ul>{(transfer.devices ?? []).map(d => <li key={d.id}>{d.name}: {d.linkChanged ? (ru ? 'ссылка отозвана или заменена — проверьте повторный импорт' : 'link revoked or replaced — verify re-import') : d.requestedAfterCutover ? (ru ? 'запрос после переключения получен' : 'request observed after cutover') : (ru ? 'запрос после переключения не наблюдался' : 'no request observed after cutover')}</li>)}</ul>
      <label className="checkbox-field account-transfer-confirmation"><input type="checkbox" checked={confirmed} onChange={e => setConfirmed(e.target.checked)} /><span>{ru ? 'Я проверил устройства и принимаю последствия очистки или отката: клиентам со старой конфигурацией потребуется обновить подписку; после начала очистки исходного узла быстрый откат недоступен.' : 'I verified devices and accept cleanup or rollback consequences: clients with cached configuration must refresh; quick rollback is unavailable after source cleanup starts.'}</span></label>
    </>}
    {transfer.lastError && <div className="form-message form-message-warning">{accountTransferError(transfer.lastError)}</div>}
    {mutation.isError && !duplicateError && <div className="form-message form-message-error">{accountTransferError(mutation.error)}</div>}
    {!terminal && <div className="form-actions">
      {next && <button className="small-button" disabled={mutation.isPending || (next === 'cleanup' && !confirmed)} onClick={() => mutation.mutate(next)}>{actions[next]}</button>}
      {transfer.lastError && ['target_applying', 'source_cleaning', 'target_cleaning'].includes(state) && <button className="small-button secondary" disabled={mutation.isPending} onClick={() => mutation.mutate('retry')}>{actions.retry}</button>}
      {['target_applying', 'target_ready', 'client_refresh_pending'].includes(state) && <button className="small-button secondary" disabled={mutation.isPending || (state === 'client_refresh_pending' && !confirmed)} onClick={() => mutation.mutate('rollback')}>{state === 'client_refresh_pending' ? actions.rollback : (ru ? 'Отменить подготовку и очистить новый узел' : 'Cancel preparation and clean target')}</button>}
    </div>}
  </div>;
}
