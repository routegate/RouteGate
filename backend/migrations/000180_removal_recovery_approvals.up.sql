CREATE TABLE routegate_dispatch.removal_recovery_approvals (
    attempt_id uuid PRIMARY KEY,
    operation_id uuid NOT NULL REFERENCES routegate_dispatch.removal_results(operation_id),
    previous_attempt_id uuid UNIQUE REFERENCES routegate_dispatch.removal_recovery_approvals(attempt_id),
    nonce text NOT NULL CHECK (nonce ~ '^[0-9a-f]{64}$'),
    decision jsonb NOT NULL CHECK (jsonb_typeof(decision) = 'object'),
    signature bytea NOT NULL CHECK (octet_length(signature) = 64),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    UNIQUE(operation_id, nonce),
    CHECK (previous_attempt_id IS DISTINCT FROM attempt_id),
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '90 seconds')
);
CREATE UNIQUE INDEX removal_recovery_one_root
ON routegate_dispatch.removal_recovery_approvals(operation_id) WHERE previous_attempt_id IS NULL;
REVOKE ALL ON TABLE routegate_dispatch.removal_recovery_approvals FROM PUBLIC;
CREATE TRIGGER rg180_recovery_approval_immutable BEFORE UPDATE OR DELETE OR TRUNCATE
ON routegate_dispatch.removal_recovery_approvals FOR EACH STATEMENT
EXECUTE FUNCTION routegate_dispatch.reject_receipt_mutation();
