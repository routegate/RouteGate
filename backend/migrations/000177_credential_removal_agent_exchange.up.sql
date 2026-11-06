CREATE TABLE routegate_dispatch.removal_claims (
 operation_id uuid PRIMARY KEY REFERENCES routegate_dispatch.held_removals(operation_id),
 claimed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 delivery_expires_at timestamptz NOT NULL
);
CREATE TABLE routegate_dispatch.removal_results (
 operation_id uuid PRIMARY KEY REFERENCES routegate_dispatch.removal_claims(operation_id),
 evidence jsonb NOT NULL CHECK (jsonb_typeof(evidence)='object'),
 received_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
REVOKE ALL ON TABLE routegate_dispatch.removal_claims,routegate_dispatch.removal_results FROM PUBLIC;
CREATE TRIGGER rg177_claim_immutable BEFORE UPDATE OR DELETE OR TRUNCATE
 ON routegate_dispatch.removal_claims FOR EACH STATEMENT
 EXECUTE FUNCTION routegate_dispatch.reject_receipt_mutation();
CREATE TRIGGER rg177_result_immutable BEFORE UPDATE OR DELETE OR TRUNCATE
 ON routegate_dispatch.removal_results FOR EACH STATEMENT
 EXECUTE FUNCTION routegate_dispatch.reject_receipt_mutation();
