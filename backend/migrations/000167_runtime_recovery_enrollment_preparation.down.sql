-- A prepared enrollment snapshot is immutable history, not activation.
-- Never erase a prepared record or its audit to downgrade silently.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM vpn_runtime_recovery_enrollment_preparations)
       OR EXISTS (SELECT 1 FROM vpn_runtime_recovery_enrollment_preparation_events) THEN
        RAISE EXCEPTION 'recovery enrollment preparation history requires reviewed archival; downgrade refused';
    END IF;
END $$;

DROP TRIGGER rg147_enrollment_event_no_truncate ON vpn_runtime_recovery_enrollment_preparation_events;
DROP TRIGGER rg147_enrollment_no_truncate ON vpn_runtime_recovery_enrollment_preparations;
DROP TRIGGER rg147_enrollment_event_immutable ON vpn_runtime_recovery_enrollment_preparation_events;
DROP FUNCTION routegate_recovery_enrollment_history_immutable();
DROP TRIGGER rg147_enrollment_prepare_audit ON vpn_runtime_recovery_enrollment_preparations;
DROP FUNCTION routegate_recovery_enrollment_prepare_audit();
DROP TRIGGER rg147_enrollment_prepare_guard ON vpn_runtime_recovery_enrollment_preparations;
DROP FUNCTION routegate_recovery_enrollment_prepare_guard();
DROP TABLE vpn_runtime_recovery_enrollment_preparation_events;
DROP TABLE vpn_runtime_recovery_enrollment_preparations;
