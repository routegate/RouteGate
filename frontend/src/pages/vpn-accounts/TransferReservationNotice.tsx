import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { getTransferReservations, transferReservationsKey } from '../../entities/vpnAccount/api/vpnAccountTransferApi';
import { getCurrentLocale } from '../../shared/i18n/i18n';

export function TransferReservationNotice({ nodeIds, blocked = false }: { nodeIds?: string[]; blocked?: boolean }) {
  const ru = getCurrentLocale() === 'ru';
  const { data, isError } = useQuery({
    queryKey: transferReservationsKey,
    queryFn: getTransferReservations,
    refetchInterval: nodeIds ? false : 15000,
    refetchOnMount: blocked ? 'always' : true,
    retry: false,
  });
  const items = (isError ? [] : data?.items ?? []).filter(item => !nodeIds || item.nodeIds.some(id => nodeIds.includes(id)));
  if (items.length === 0 && !blocked) return null;

  return (
    <div className="form-message form-message-warning" role={blocked ? 'alert' : 'status'}>
      <strong>{items.length > 0
        ? (ru ? 'Переносы аккаунтов блокируют изменения на зарезервированных узлах' : 'Account transfers block changes on reserved nodes')
        : (ru ? 'Операция заблокирована переносом аккаунта' : 'An account transfer is blocking this operation')}</strong>
      {items.length === 0 && <div>{ru
        ? 'Откройте раздел VPN-аккаунтов и проверьте незавершённые переносы в разделе «Маршрутизация».'
        : 'Open VPN accounts and check Routing for unfinished transfers.'}</div>}
      {items.length > 0 && <ul>{items.map(item => (
        <li key={item.accountId}>
          <Link className="text-link" to={`/vpn-accounts/${encodeURIComponent(item.accountId)}/routing`}>{item.accountName || item.accountId}</Link>
          {' · '}{item.sourceName} → {item.targetName}
          {' · '}{ru ? 'Зарезервированы' : 'Reserved'} {item.nodeNames.join(', ')}
          {' · '}{item.state === 'client_refresh_pending'
            ? (ru ? 'Проверьте клиентов и завершите перенос' : 'Verify clients and finish the transfer')
            : (ru ? 'Откройте перенос и проверьте следующий шаг' : 'Open the transfer to see the next step')}
        </li>
      ))}</ul>}
    </div>
  );
}
