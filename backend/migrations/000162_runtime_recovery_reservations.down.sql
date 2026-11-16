-- A durable reservation is evidence, not expendable temporary state.
-- Never discard any held node or historical evidence on rollback.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM vpn_runtime_recovery_reservations) THEN
        RAISE EXCEPTION 'recovery reservations require reviewed archival; downgrade refused';
    END IF;
END $$;

-- Restore exactly the prior RG-140 guards while the table still exists.
CREATE OR REPLACE FUNCTION routegate_revocation_node_guard(node uuid) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 IF node IS NULL THEN RETURN; END IF;
 PERFORM id FROM servers WHERE id=node FOR UPDATE;
 IF EXISTS(SELECT 1 FROM vpn_credential_revocations WHERE server_id=node AND state='prepared') THEN
 RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='node_reserved_for_revocation'; END IF;
END; $$;

CREATE OR REPLACE FUNCTION routegate_revocation_routing_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM vpn_credential_revocations WHERE state='prepared') THEN RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='routing_reserved_for_revocation'; END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END; $$;

CREATE OR REPLACE FUNCTION routegate_revocation_manager_update_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.operation='apply' THEN
 PERFORM pg_advisory_xact_lock(140159);
 IF EXISTS(SELECT 1 FROM vpn_credential_revocations WHERE state='prepared') THEN RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='manager_update_reserved_for_revocation'; END IF;
 END IF; RETURN NEW;
END; $$;

DROP TRIGGER rg142_revocation_insert ON vpn_credential_revocations;
DROP FUNCTION routegate_runtime_recovery_revocation_guard();
DROP TRIGGER rg142_reservation_insert ON vpn_runtime_recovery_reservations;
DROP FUNCTION routegate_runtime_recovery_reservation_guard();
DROP TRIGGER rg142_reservation_immutable ON vpn_runtime_recovery_reservations;
DROP FUNCTION routegate_runtime_recovery_immutable();
DROP TABLE vpn_runtime_recovery_reservations;
