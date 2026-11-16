-- A downgrade would reintroduce the FK/cancellation deadlock and could
-- make an ambiguous dispatch state unsafe. Keep the safety fix forward-only.
DO $$
BEGIN
 RAISE EXCEPTION USING ERRCODE='P0141',MESSAGE='revocation_dispatch_lock_order_downgrade_forbidden';
END; $$;
