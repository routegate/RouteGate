CREATE TABLE routegate_dispatch.retired_vless_credentials (
    digest bytea PRIMARY KEY CHECK (octet_length(digest) = 32),
    operation_id uuid NOT NULL UNIQUE REFERENCES public.vpn_credential_revocations(id),
    retired_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
REVOKE ALL ON TABLE routegate_dispatch.retired_vless_credentials FROM PUBLIC;

CREATE TRIGGER rg178_retired_immutable BEFORE UPDATE OR DELETE OR TRUNCATE
ON routegate_dispatch.retired_vless_credentials FOR EACH STATEMENT
EXECUTE FUNCTION routegate_dispatch.reject_receipt_mutation();

-- Go accepts case variants of JSON field names. Inspect every matching member
-- so a stored config cannot bypass the guard through a second spelling.
CREATE FUNCTION routegate_dispatch.config_members(document jsonb, field_name text)
RETURNS SETOF jsonb LANGUAGE sql IMMUTABLE SET search_path = pg_catalog, pg_temp AS $$
    SELECT value
    FROM jsonb_each(CASE WHEN jsonb_typeof(document) = 'object' THEN document ELSE '{}'::jsonb END)
    WHERE translate(key, 'ABCDEFGHIJKLMNOPQRSTUVWXYZſ', 'abcdefghijklmnopqrstuvwxyzs') = field_name;
$$;

CREATE FUNCTION routegate_dispatch.vless_config_digests(config jsonb)
RETURNS SETOF bytea LANGUAGE sql IMMUTABLE SET search_path = pg_catalog, pg_temp AS $$
    WITH runtime_config(document) AS (
        -- Older Agents also accept a plain sing-box configuration.
        SELECT config
        UNION ALL
        SELECT routegate_dispatch.config_members(config, 'singbox')
    ), identifiers(value) AS (
        SELECT identifier
        FROM routegate_dispatch.config_members(config, 'vpnaccounts') AS accounts
        CROSS JOIN LATERAL jsonb_path_query(accounts, '$[*]') AS account
        CROSS JOIN LATERAL routegate_dispatch.config_members(account, 'vlessuuid') AS identifier
        UNION ALL
        SELECT identifier
        FROM runtime_config
        CROSS JOIN LATERAL routegate_dispatch.config_members(document, 'inbounds') AS inbounds
        CROSS JOIN LATERAL jsonb_path_query(inbounds, '$[*]') AS inbound
        CROSS JOIN LATERAL routegate_dispatch.config_members(inbound, 'users') AS users
        CROSS JOIN LATERAL jsonb_path_query(users, '$[*]') AS client
        CROSS JOIN LATERAL routegate_dispatch.config_members(client, 'uuid') AS identifier
    ), normalized(digits) AS (
        SELECT translate(regexp_replace(lower(value #>> '{}'), '^urn:uuid:', ''), '-{}', '')
        FROM identifiers
        WHERE jsonb_typeof(value) = 'string'
    )
    SELECT sha256(decode(digits, 'hex')) FROM normalized WHERE digits ~ '^[0-9a-f]{32}$';
$$;

-- Guard checks use fresh snapshots after taking the lock. A transaction with a
-- fixed snapshot could otherwise miss a retirement committed while it waited.
CREATE FUNCTION routegate_dispatch.require_retirement_read_committed()
RETURNS void LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    IF current_setting('transaction_isolation') <> 'read committed' THEN
        RAISE EXCEPTION USING ERRCODE = 'P0141', MESSAGE = 'VLESS retirement checks require read committed isolation';
    END IF;
END;
$$;

CREATE FUNCTION routegate_dispatch.reject_retired_vless()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
DECLARE
    config jsonb;
    blocked boolean;
BEGIN
    IF TG_TABLE_NAME = 'config_apply_jobs' THEN
        IF NEW.status NOT IN ('pending', 'in_progress', 'succeeded', 'held_for_revocation') THEN
            RETURN NEW;
        END IF;
    ELSIF TG_TABLE_NAME = 'servers' THEN
        IF NEW.active_config_version_id IS NULL THEN
            RETURN NEW;
        END IF;
    END IF;

    PERFORM routegate_dispatch.require_retirement_read_committed();
    -- The implicit ROW EXCLUSIVE lock of a retirement INSERT conflicts
    -- with this SHARE lock through commit in both transaction orders.
    LOCK TABLE routegate_dispatch.retired_vless_credentials IN SHARE MODE;
    IF TG_TABLE_NAME = 'vpn_accounts' THEN
        SELECT EXISTS (
            SELECT 1 FROM routegate_dispatch.retired_vless_credentials
            WHERE digest = sha256(uuid_send(NEW.vless_uuid))
        ) INTO blocked;
    ELSE
        CASE TG_TABLE_NAME
            WHEN 'config_versions' THEN config := NEW.rendered_config;
            WHEN 'config_apply_jobs' THEN
                SELECT rendered_config INTO config FROM public.config_versions WHERE id = NEW.config_version_id;
            WHEN 'servers' THEN
                SELECT rendered_config INTO config FROM public.config_versions WHERE id = NEW.active_config_version_id;
        END CASE;
        SELECT EXISTS (
            SELECT 1 FROM routegate_dispatch.vless_config_digests(config) AS credential(digest)
            JOIN routegate_dispatch.retired_vless_credentials USING (digest)
        ) INTO blocked;
    END IF;

    IF blocked THEN
        RAISE EXCEPTION USING ERRCODE = 'P0141', MESSAGE = 'retired VLESS credential cannot be used';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER rg178_account BEFORE INSERT OR UPDATE OF vless_uuid, status, server_id ON public.vpn_accounts
FOR EACH ROW EXECUTE FUNCTION routegate_dispatch.reject_retired_vless();
CREATE TRIGGER rg178_config_version BEFORE INSERT OR UPDATE OF rendered_config ON public.config_versions
FOR EACH ROW EXECUTE FUNCTION routegate_dispatch.reject_retired_vless();
CREATE TRIGGER rg178_apply_job BEFORE INSERT OR UPDATE OF config_version_id, status ON public.config_apply_jobs
FOR EACH ROW EXECUTE FUNCTION routegate_dispatch.reject_retired_vless();
CREATE TRIGGER rg178_active_config BEFORE INSERT OR UPDATE OF active_config_version_id ON public.servers
FOR EACH ROW EXECUTE FUNCTION routegate_dispatch.reject_retired_vless();

CREATE FUNCTION routegate_dispatch.check_vless_retirement_isolation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    PERFORM routegate_dispatch.require_retirement_read_committed();
    RETURN NULL;
END;
$$;

CREATE TRIGGER rg178_retired_isolation BEFORE INSERT ON routegate_dispatch.retired_vless_credentials
FOR EACH STATEMENT EXECUTE FUNCTION routegate_dispatch.check_vless_retirement_isolation();

-- Check the final transaction state so a trusted coordinator can replace the
-- account identity and promote the clean candidate in the same transaction.
CREATE FUNCTION routegate_dispatch.verify_vless_retirement()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM public.vpn_accounts WHERE sha256(uuid_send(vless_uuid)) = NEW.digest
    ) OR EXISTS (
        SELECT 1 FROM (
            SELECT active_config_version_id AS id FROM public.servers WHERE active_config_version_id IS NOT NULL
            UNION
            SELECT config_version_id FROM public.config_apply_jobs
            WHERE status IN ('pending', 'in_progress', 'held_for_revocation')
        ) AS usable
        JOIN public.config_versions USING (id)
        CROSS JOIN LATERAL routegate_dispatch.vless_config_digests(rendered_config) AS credential(digest)
        WHERE credential.digest = NEW.digest
    ) THEN
        RAISE EXCEPTION USING ERRCODE = 'P0141', MESSAGE = 'VLESS credential remains in an account, active config, or open job';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER rg178_retired_verify AFTER INSERT ON routegate_dispatch.retired_vless_credentials
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION routegate_dispatch.verify_vless_retirement();

REVOKE ALL ON FUNCTION
    routegate_dispatch.config_members(jsonb, text),
    routegate_dispatch.vless_config_digests(jsonb),
    routegate_dispatch.require_retirement_read_committed(),
    routegate_dispatch.reject_retired_vless(),
    routegate_dispatch.check_vless_retirement_isolation(),
    routegate_dispatch.verify_vless_retirement()
FROM PUBLIC;
