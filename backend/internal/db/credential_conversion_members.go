package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var ErrCredentialConversionMembers = errors.New("credential conversion role has members")

type credentialConversionQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// RequireNoCredentialConversionMembers is an unwired, SELECT-only preflight.
// It rejects every incoming membership, including NOLOGIN and disabled-option
// members. The caller supplies an independently resolved, stable role OID/name.
// Success is only a catalog snapshot, NOT authorization to change credentials:
// concurrent grants, superusers, other ACLs, existing SET ROLE sessions and
// prepared transactions require separate administrative controls.
func RequireNoCredentialConversionMembers(ctx context.Context, q credentialConversionQuerier, roleOID uint32, roleName string) error {
	if roleOID == 0 || roleName == "" {
		return errors.New("credential conversion requires a role OID and name")
	}
	var found bool
	var members int64
	err := q.QueryRow(ctx, `WITH RECURSIVE target AS (
 SELECT oid FROM pg_catalog.pg_roles WHERE oid=$1::oid AND rolname=$2
), reachable(oid) AS (
 SELECT oid FROM target
 UNION
 SELECT m.member FROM pg_catalog.pg_auth_members m
 JOIN reachable r ON m.roleid=r.oid
)
SELECT EXISTS(SELECT 1 FROM target),
 (SELECT count(*) FROM reachable WHERE oid<>$1::oid)`, roleOID, roleName).Scan(&found, &members)
	if err != nil {
		return fmt.Errorf("inspect credential conversion memberships: %w", err)
	}
	if !found {
		return errors.New("credential conversion role identity missing or changed")
	}
	if members != 0 {
		return fmt.Errorf("%w: %d direct or indirect roles", ErrCredentialConversionMembers, members)
	}
	return nil
}
