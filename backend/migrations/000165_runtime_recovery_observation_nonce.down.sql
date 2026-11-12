-- Do not silently erase any issued or resolved nonce or its audit trail.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM vpn_runtime_recovery_observation_nonces) OR
       EXISTS (SELECT 1 FROM vpn_runtime_recovery_observation_nonce_events) THEN
        RAISE EXCEPTION 'recovery observation nonce history requires reviewed archival; downgrade refused';
    END IF;
END $$;

DROP TRIGGER rg145_nonce_events_no_truncate ON vpn_runtime_recovery_observation_nonce_events;
DROP TRIGGER rg145_nonce_no_truncate ON vpn_runtime_recovery_observation_nonces;
DROP TRIGGER rg145_nonce_events_immutable ON vpn_runtime_recovery_observation_nonce_events;
DROP FUNCTION routegate_recovery_observation_nonce_immutable();
DROP TRIGGER rg145_observation_nonce_audit ON vpn_runtime_recovery_observation_nonces;
DROP FUNCTION routegate_recovery_observation_nonce_audit();
DROP TRIGGER rg145_observation_nonce_guard ON vpn_runtime_recovery_observation_nonces;
DROP FUNCTION routegate_recovery_observation_nonce_guard();
DROP TABLE vpn_runtime_recovery_observation_nonce_events;
DROP TABLE vpn_runtime_recovery_observation_nonces;
