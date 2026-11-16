-- Never lose a reserved task identity: an Agent might have observed it.
DO $$
BEGIN
 IF EXISTS (SELECT 1 FROM vpn_credential_revocation_task_reservations) THEN
  RAISE EXCEPTION USING ERRCODE='P0141', MESSAGE='revocation_task_identity_downgrade_requires_reconciliation';
 END IF;
END; $$;
DROP TRIGGER rg145_task_identity ON vpn_credential_revocation_task_reservations;
DROP FUNCTION routegate_revocation_task_identity_guard();
DROP TABLE vpn_credential_revocation_task_reservations;
