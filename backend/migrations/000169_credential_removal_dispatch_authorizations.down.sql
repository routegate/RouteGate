DO $$ BEGIN
 LOCK TABLE vpn_credential_removal_dispatch_authorizations IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM vpn_credential_removal_dispatch_authorizations) THEN
  RAISE EXCEPTION 'removal_dispatch_authorizations_retained';
 END IF;
 DROP TABLE vpn_credential_removal_dispatch_authorizations;
 DROP FUNCTION routegate_removal_dispatch_authorization_guard();
END; $$;
