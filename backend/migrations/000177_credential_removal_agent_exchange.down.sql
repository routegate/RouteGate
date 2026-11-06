DO $$
BEGIN
 IF EXISTS (SELECT 1 FROM routegate_dispatch.removal_claims) THEN
  RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='held_revocation_claim_retained';
 END IF;
END; $$;
DROP TABLE routegate_dispatch.removal_results;
DROP TABLE routegate_dispatch.removal_claims;
