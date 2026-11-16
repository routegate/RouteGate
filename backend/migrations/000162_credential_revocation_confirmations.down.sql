-- Explicitly fail closed: never discard an audit-bearing confirmation.
DO $$
BEGIN
 IF EXISTS(SELECT 1 FROM vpn_credential_revocation_confirmations) THEN
  RAISE EXCEPTION USING ERRCODE='P0141', MESSAGE='revocation_confirmations_require_archival';
 END IF;
END; $$;
DROP TRIGGER rg142_confirmation ON vpn_credential_revocation_confirmations;
DROP FUNCTION routegate_revocation_confirmation_guard();
DROP TABLE vpn_credential_revocation_confirmations;
