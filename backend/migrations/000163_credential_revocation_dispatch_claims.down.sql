-- A possible dispatch has an unknown outcome even when no Agent
-- acknowledgement was received. A downgrade may never erase that fence.
DO $$
BEGIN
 IF EXISTS(SELECT 1 FROM vpn_credential_revocation_dispatch_claims) THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_dispatch_claims_require_reconciliation';
 END IF;
END; $$;
DROP TRIGGER rg143_dispatch_cancel ON vpn_credential_revocations;
DROP FUNCTION routegate_revocation_dispatch_cancel_guard();
DROP TRIGGER rg143_dispatch_claim ON vpn_credential_revocation_dispatch_claims;
DROP FUNCTION routegate_revocation_dispatch_claim_guard();
DROP TABLE vpn_credential_revocation_dispatch_claims;
