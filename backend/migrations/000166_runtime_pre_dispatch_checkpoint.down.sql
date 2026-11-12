-- Destructive downgrade is forbidden whenever any pre-dispatch checkpoint
-- or its audit exists. Retained history needs a separately reviewed archival
-- protocol; never erase it to satisfy an older schema migration.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM vpn_runtime_pre_dispatch_checkpoints)
       OR EXISTS (SELECT 1 FROM vpn_runtime_pre_dispatch_checkpoint_events) THEN
        RAISE EXCEPTION 'pre-dispatch checkpoint history requires reviewed archival; downgrade refused';
    END IF;
END $$;

DROP TRIGGER rg146_checkpoint_version_identity ON config_versions;
DROP FUNCTION routegate_pre_dispatch_checkpoint_version_guard();
DROP TRIGGER rg146_checkpoint_operation_job_identity ON agent_operation_jobs;
DROP TRIGGER rg146_checkpoint_config_job_identity ON config_apply_jobs;
DROP FUNCTION routegate_pre_dispatch_checkpoint_job_identity_guard();

DROP TRIGGER rg146_checkpoint_event_no_truncate ON vpn_runtime_pre_dispatch_checkpoint_events;
DROP TRIGGER rg146_checkpoint_no_truncate ON vpn_runtime_pre_dispatch_checkpoints;
DROP TRIGGER rg146_checkpoint_event_immutable ON vpn_runtime_pre_dispatch_checkpoint_events;
DROP FUNCTION routegate_pre_dispatch_checkpoint_immutable();
DROP TRIGGER rg146_checkpoint_audit ON vpn_runtime_pre_dispatch_checkpoints;
DROP FUNCTION routegate_pre_dispatch_checkpoint_audit();
DROP TRIGGER rg146_checkpoint_guard ON vpn_runtime_pre_dispatch_checkpoints;
DROP FUNCTION routegate_pre_dispatch_checkpoint_guard();
DROP TABLE vpn_runtime_pre_dispatch_checkpoint_events;
DROP TABLE vpn_runtime_pre_dispatch_checkpoints;
