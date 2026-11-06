DO $$
BEGIN
    LOCK TABLE routegate_dispatch.retired_vless_credentials IN ACCESS EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM routegate_dispatch.retired_vless_credentials) THEN
        RAISE EXCEPTION USING ERRCODE = 'P0141', MESSAGE = 'cannot remove retained VLESS retirement history';
    END IF;
END;
$$;
DROP TRIGGER rg178_active_config ON public.servers;
DROP TRIGGER rg178_apply_job ON public.config_apply_jobs;
DROP TRIGGER rg178_config_version ON public.config_versions;
DROP TRIGGER rg178_account ON public.vpn_accounts;
DROP TABLE routegate_dispatch.retired_vless_credentials;
DROP FUNCTION routegate_dispatch.verify_vless_retirement();
DROP FUNCTION routegate_dispatch.check_vless_retirement_isolation();
DROP FUNCTION routegate_dispatch.reject_retired_vless();
DROP FUNCTION routegate_dispatch.require_retirement_read_committed();
DROP FUNCTION routegate_dispatch.vless_config_digests(jsonb);
DROP FUNCTION routegate_dispatch.config_members(jsonb, text);
