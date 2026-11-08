DO $$
BEGIN
    LOCK TABLE routegate_dispatch.removal_recovery_approvals IN ACCESS EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM routegate_dispatch.removal_recovery_approvals) THEN
        RAISE EXCEPTION USING ERRCODE = 'P0141', MESSAGE = 'removal recovery approval history retained';
    END IF;
END;
$$;
DROP TABLE routegate_dispatch.removal_recovery_approvals;
