import { getCurrentLocale } from '../../shared/i18n/i18n';
const reasons: [string[], string, string][] = [
  [['node_role_protocol_or_agent_not_ready'], 'Проверьте роль узла, VLESS/Reality и работающий Agent. Дождитесь нового подтверждённого heartbeat перед повтором.', 'Check the VPN node role, VLESS/Reality and running Agent. Wait for a fresh authenticated heartbeat before retrying.'],
  [['only_applied_vless_accounts_supported'], 'Пока перенос поддерживает только уже применённый VLESS/Reality. Другие протоколы и несохранённые изменения сначала требуют отдельной подготовки.', 'Transfers currently support applied VLESS/Reality only. Other protocols and pending changes require separate preparation.'],
  [['source_not_applied', 'source_apply_proof_missing', 'apply_proof_missing'], 'Сначала подтвердите применение конфигурации исходного узла через Agent.', 'First confirm the source configuration was applied through the Agent.'],
  [['node_has_pending_configuration_changes', 'node_has_unapplied_accounts'], 'На узле есть неприменённые изменения. Завершите или отмените их перед переносом; перенос не должен применять чужие изменения.', 'This node has pending configuration changes. Finish or discard them before transferring this account.'],
  [['node_has_conflicting_operation', 'account_has_active_transfer', 'transfer_conflict', 'node_reserved_for_transfer'], 'Узел или аккаунт занят другой операцией. Продолжите её или дождитесь завершения.', 'This account or node is reserved by another operation. Continue it or wait for completion.'],
  [['public_listener_unreachable'], 'VPN-порт нового узла недоступен с Manager. Проверьте адрес, порт, firewall и работу VPN, затем повторите проверку.', 'The VPN port is unreachable from the Manager. Check its address, port, firewall and VPN runtime, then verify again.'],
  [['apply_in_progress', 'wait_for_target_apply_before_rollback'], 'Agent ещё выполняет применение. Дождитесь результата; исходный доступ сохранён.', 'The Agent is still applying configuration. Wait for the result; source access is retained.'],
  [['apply_failed_or_version_changed'], 'Проверьте результат применения. Неудачное применение можно повторить; при смене версии требуется восстановить проверенное состояние.', 'Check the apply result. A failed apply can be retried; a changed version requires restoring verified state.'],
  [['runtime_evidence_missing', 'runtime_listener_not_verified'], 'Нет подтверждения работающего VPN-слушателя. Проверьте отчёт Agent и включённое управление службами.', 'A working VPN listener has not been confirmed. Check the Agent report and enabled service control.'],
];
function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

// Only collapse the same known reason or the same raw error. Different unknown
// errors may share fallback copy, but still represent separate failures.
export function accountTransferErrorIdentity(error: unknown): string {
  const message = errorMessage(error);
  for (const [codes] of reasons) {
    const code = codes.find(code => message.includes(code));
    if (code) return code;
  }
  return message;
}

export function accountTransferError(error: unknown): string {
  const message = errorMessage(error);
  const ru = getCurrentLocale() === 'ru';
  for (const [codes, russian, english] of reasons) if (codes.some(code => message.includes(code))) return ru ? russian : english;
  return ru ? 'Действие не выполнено. Проверьте состояние операции и результат применения, затем повторите проверку.' : 'The action did not complete. Check the operation and apply result, then verify again.';
}
