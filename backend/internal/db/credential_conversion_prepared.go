package db

import (
	"context"
	"errors"
	"fmt"
)

var ErrCredentialConversionPrepared = errors.New("prepared transactions block credential conversion")

// RequireNoCredentialConversionPrepared is an unwired, SELECT-only preflight.
// Prepared transactions outlive their sessions, so an empty pg_stat_activity
// scan does not establish quiescence. Check the old role across the cluster and
// every owner in the pinned Manager database, where an alternate login may
// have prepared writes. Success is only a point-in-time snapshot.
func RequireNoCredentialConversionPrepared(ctx context.Context, q credentialConversionQuerier, roleOID uint32, roleName string, databaseOID uint32, databaseName string) error {
	if roleOID == 0 || roleName == "" {
		return errors.New("credential conversion requires a role OID and name")
	}
	if databaseOID == 0 || databaseName == "" {
		return errors.New("credential conversion requires a database OID and name")
	}
	var roleFound, databaseFound bool
	var prepared int64
	err := q.QueryRow(ctx, `WITH target_role AS (
 SELECT rolname FROM pg_catalog.pg_roles WHERE oid=$1::oid AND rolname=$2
), target_database AS (
 SELECT datname FROM pg_catalog.pg_database WHERE oid=$3::oid AND datname=$4
)
SELECT EXISTS(SELECT 1 FROM target_role),
 EXISTS(SELECT 1 FROM target_database),
 (SELECT count(*) FROM pg_catalog.pg_prepared_xacts p
  WHERE p.owner=(SELECT rolname FROM target_role)
     OR p.database=(SELECT datname FROM target_database))`, roleOID, roleName, databaseOID, databaseName).Scan(&roleFound, &databaseFound, &prepared)
	if err != nil {
		return fmt.Errorf("inspect credential conversion prepared transactions: %w", err)
	}
	if !roleFound {
		return errors.New("credential conversion role identity missing or changed")
	}
	if !databaseFound {
		return errors.New("credential conversion database identity missing or changed")
	}
	if prepared != 0 {
		return fmt.Errorf("%w: %d transactions", ErrCredentialConversionPrepared, prepared)
	}
	return nil
}
