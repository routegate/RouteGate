DO $$ BEGIN
 LOCK TABLE vpn_credential_removal_dispatch_stages IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM vpn_credential_removal_dispatch_stages) THEN
  RAISE EXCEPTION 'removal_dispatch_stages_retained';
 END IF;
 DROP TABLE vpn_credential_removal_dispatch_stages;
 DROP FUNCTION routegate_removal_dispatch_stage_guard();
END; $$;
