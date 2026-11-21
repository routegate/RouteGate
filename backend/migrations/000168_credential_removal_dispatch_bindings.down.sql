DO $$ BEGIN
 LOCK TABLE vpn_credential_revocation_dispatch_bindings IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM vpn_credential_revocation_dispatch_bindings) THEN
  RAISE EXCEPTION 'removal_dispatch_bindings_retained'; END IF;
 DROP TABLE vpn_credential_revocation_dispatch_bindings;
 DROP FUNCTION routegate_removal_dispatch_binding_guard();
END; $$;
