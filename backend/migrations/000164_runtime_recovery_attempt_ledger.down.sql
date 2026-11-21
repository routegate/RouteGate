-- Do not erase durable recovery intent after a consumed human challenge.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM vpn_runtime_recovery_attempts) THEN
        RAISE EXCEPTION 'recovery attempt history must be archived by an independently reviewed protocol; downgrade refused';
    END IF;
END $$;

DROP TRIGGER rg144_challenge_consumed_attempt ON vpn_runtime_recovery_challenges;
DROP FUNCTION routegate_recovery_attempt_on_consumed_challenge();
DROP TRIGGER rg144_attempt_no_truncate ON vpn_runtime_recovery_attempts;
DROP TRIGGER rg144_attempt_no_change ON vpn_runtime_recovery_attempts;
DROP FUNCTION routegate_recovery_attempt_immutable();
DROP TRIGGER rg144_attempt_insert_guard ON vpn_runtime_recovery_attempts;
DROP FUNCTION routegate_recovery_attempt_guard();
DROP TABLE vpn_runtime_recovery_attempts;
