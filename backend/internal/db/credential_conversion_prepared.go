package db

import (
	"context"
	"errors"
	"fmt"
)

var ErrCredentialConversionPrepared = errors.New("credential conversion role owns prepared transactions")

// RequireNoCredentialConversionPrepared is an unwired, SELECT-only preflight.
// Prepared transactions outlive their sessions, so an empty pg_stat_activity
// scan does not establish quiescence. Check the entire cluster, not just the
// connected database. Never decide COMMIT/ROLLBACK PREPARED on behalf of an
// external transaction coordinator. Success is only a point-in-time snapshot;
// membership, concurrent preparation and administrative access need other gates.
func RequireNoCredentialConversionPrepared(ctx context.Context, q credentialConversionQuerier, roleOID uint32, roleName string) error {
	if roleOID == 0 || roleName == "" {
		return errors.New("credential conversion requires a role OID and name")
	}
	var found bool
	var prepared int64
	err := q.QueryRow(ctx, `WITH target AS (
 SELECT rolname FROM pg_catalog.pg_roles WHERE oid=$1::oid AND rolname=$2
)
SELECT EXISTS(SELECT 1 FROM target),
 (SELECT count(*) FROM pg_catalog.pg_prepared_xacts p JOIN target t ON p.owner=t.rolname)`, roleOID, roleName).Scan(&found, &prepared)
	if err != nil {
		return fmt.Errorf("inspect credential conversion prepared transactions: %w", err)
	}
	if !found {
		return errors.New("credential conversion role identity missing or changed")
	}
	if prepared != 0 {
		return fmt.Errorf("%w: %d transactions", ErrCredentialConversionPrepared, prepared)
	}
	return nil
}
