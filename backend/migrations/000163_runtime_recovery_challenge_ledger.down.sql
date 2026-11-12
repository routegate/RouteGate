-- No downgrade is allowed to erase a potentially issued/consumed challenge.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM vpn_runtime_recovery_challenges) OR
       EXISTS (SELECT 1 FROM vpn_runtime_recovery_challenge_events) THEN
        RAISE EXCEPTION 'recovery challenge history requires reviewed archival; downgrade refused';
    END IF;
END $$;

DROP TRIGGER rg143_event_no_truncate ON vpn_runtime_recovery_challenge_events;
DROP TRIGGER rg143_challenge_no_truncate ON vpn_runtime_recovery_challenges;
DROP TRIGGER rg143_event_immutable ON vpn_runtime_recovery_challenge_events;
DROP FUNCTION routegate_recovery_challenge_history_immutable();
DROP TRIGGER rg143_challenge_audit ON vpn_runtime_recovery_challenges;
DROP FUNCTION routegate_recovery_challenge_audit();
DROP TRIGGER rg143_challenge_guard ON vpn_runtime_recovery_challenges;
DROP FUNCTION routegate_recovery_challenge_ledger_guard();
DROP FUNCTION routegate_recovery_challenge_actor_guard(uuid,uuid);
DROP TABLE vpn_runtime_recovery_challenge_events;
DROP TABLE vpn_runtime_recovery_challenges;
