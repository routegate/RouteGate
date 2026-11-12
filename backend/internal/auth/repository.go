package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrInvalidCredentials = errors.New("invalid credentials")

var BuiltInPermissions = []string{
	"system:manage",
	"users:read",
	"users:create",
	"users:update",
	"users:disable",
	"users:delete",

	"roles:read",
	"roles:assign",

	"vpn_users:read",
	"vpn_users:create",
	"vpn_users:update",
	"vpn_users:disable",

	"servers:read",
	"servers:create",
	"servers:update",
	"servers:delete",

	"agents:read",
	"agents:register",
	"agents:disable",

	"configs:read",
	"configs:render",
	"configs:validate",
	"configs:apply",
	"configs:delete",
	"configs:rollback",
	PermissionRuntimeRecoveryAuthorize,

	"routing_profiles:read",
	"routing_profiles:create",
	"routing_profiles:update",
	"routing_profiles:delete",

	"traffic:read",
	"audit:read",

	"licenses:read",
	"licenses:manage",

	"portal:access",

	"agent:connect",
	"agent:heartbeat",
	"agent:report_status",
	"agent:receive_tasks",
}

var BuiltInRoles = map[string][]string{
	"super_admin": BuiltInPermissions,

	"admin": without(
		BuiltInPermissions,
		"system:manage",
		PermissionRuntimeRecoveryAuthorize,
		"licenses:manage",
		"portal:access",
		"agent:connect",
		"agent:heartbeat",
		"agent:report_status",
		"agent:receive_tasks",
	),

	"operator": {
		"users:read",
		"vpn_users:read",
		"vpn_users:create",
		"vpn_users:update",
		"vpn_users:disable",
		"servers:read",
		"agents:read",
		"configs:read",
		"traffic:read",
		"audit:read",
	},

	"read_only": {
		"users:read",
		"roles:read",
		"vpn_users:read",
		"servers:read",
		"agents:read",
		"configs:read",
		"routing_profiles:read",
		"traffic:read",
		"audit:read",
		"licenses:read",
	},

	"vpn_user": {
		"portal:access",
	},

	"agent": {
		"agent:connect",
		"agent:heartbeat",
		"agent:report_status",
		"agent:receive_tasks",
	},
}

type RoleSeed struct {
	Code        string
	Name        string
	Description string
}

var roleSeeds = []RoleSeed{
	{
		Code:        "super_admin",
		Name:        "SuperAdmin",
		Description: "Full access to all Manager permissions.",
	},
	{
		Code:        "admin",
		Name:        "Admin",
		Description: "Operational Manager administration access.",
	},
	{
		Code:        "operator",
		Name:        "Operator",
		Description: "Day-to-day operational access.",
	},
	{
		Code:        "read_only",
		Name:        "ReadOnly",
		Description: "Read-only Manager access.",
	},
	{
		Code:        "vpn_user",
		Name:        "VpnUser",
		Description: "User Portal access.",
	},
	{
		Code:        "agent",
		Name:        "Agent",
		Description: "Agent API access.",
	},
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) EnsureBuiltIns(ctx context.Context) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, code := range BuiltInPermissions {
		_, err := tx.Exec(
			ctx,
			`
			INSERT INTO permissions (code, name)
			VALUES ($1, $2)
			ON CONFLICT (code)
			DO UPDATE SET name = EXCLUDED.name
			`,
			code,
			code,
		)
		if err != nil {
			return err
		}
	}

	for _, role := range roleSeeds {
		_, err := tx.Exec(
			ctx,
			`
			INSERT INTO roles (code, name, description, built_in)
			VALUES ($1, $2, $3, true)
			ON CONFLICT (code)
			DO UPDATE SET
				name = EXCLUDED.name,
				description = EXCLUDED.description,
				built_in = true,
				updated_at = now()
			`,
			role.Code,
			role.Name,
			role.Description,
		)
		if err != nil {
			return err
		}

		for _, permission := range BuiltInRoles[role.Code] {
			_, err := tx.Exec(
				ctx,
				`
				INSERT INTO role_permissions (role_id, permission_id)
				SELECT r.id, p.id
				FROM roles r, permissions p
				WHERE r.code = $1 AND p.code = $2
				ON CONFLICT DO NOTHING
				`,
				role.Code,
				permission,
			)
			if err != nil {
				return err
			}
		}
	}

	return tx.Commit(ctx)
}

func (r *Repository) HasSuperAdmin(ctx context.Context) (bool, error) {
	var exists bool

	err := r.pool.QueryRow(
		ctx,
		`
		SELECT EXISTS (
			SELECT 1
			FROM users u
			JOIN user_roles ur ON ur.user_id = u.id
			JOIN roles r ON r.id = ur.role_id
			WHERE r.code = 'super_admin'
		)
		`,
	).Scan(&exists)

	return exists, err
}

func (r *Repository) CreateBootstrapSuperAdmin(ctx context.Context, email, username, password, displayName string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var id string

	err = tx.QueryRow(
		ctx,
		`
		INSERT INTO users (
			email,
			username,
			password_hash,
			display_name,
			user_type,
			status
		)
		VALUES (
			$1,
			NULLIF($2, ''),
			$3,
			NULLIF($4, ''),
			'human',
			'active'
		)
		RETURNING id::text
		`,
		email,
		username,
		hash,
		displayName,
	).Scan(&id)
	if err != nil {
		return err
	}

	_, err = tx.Exec(
		ctx,
		`
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1::uuid, id
		FROM roles
		WHERE code = 'super_admin'
		`,
		id,
	)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *Repository) Authenticate(ctx context.Context, login, password, userAgent, ip string, ttl time.Duration) (LoginResponse, error) {
	var id string
	var hash string
	var status string

	err := r.pool.QueryRow(
		ctx,
		`
		SELECT
			id::text,
			COALESCE(password_hash, ''),
			status
		FROM users
		WHERE lower(email) = lower($1)
		   OR lower(COALESCE(username, '')) = lower($1)
		`,
		login,
	).Scan(&id, &hash, &status)
	if err != nil {
		return LoginResponse{}, ErrInvalidCredentials
	}

	if status != "active" || hash == "" || !VerifyPassword(hash, password) {
		return LoginResponse{}, ErrInvalidCredentials
	}

	token, err := randomToken()
	if err != nil {
		return LoginResponse{}, err
	}

	expiresAt := time.Now().UTC().Add(ttl)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return LoginResponse{}, err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(
		ctx,
		`
		INSERT INTO auth_sessions (
			user_id,
			token_hash,
			expires_at,
			user_agent,
			ip_address
		)
		VALUES (
			$1,
			$2,
			$3,
			NULLIF($4, ''),
			NULLIF($5, '')
		)
		`,
		id,
		TokenHash(token),
		expiresAt,
		userAgent,
		ip,
	)
	if err != nil {
		return LoginResponse{}, err
	}

	_, err = tx.Exec(
		ctx,
		`
		UPDATE users
		SET
			last_login_at = now(),
			updated_at = now()
		WHERE id = $1
		`,
		id,
	)
	if err != nil {
		return LoginResponse{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return LoginResponse{}, err
	}

	profile, err := r.ProfileByID(ctx, id)
	if err != nil {
		return LoginResponse{}, err
	}

	return LoginResponse{
		Token:     token,
		ExpiresAt: expiresAt,
		User:      profile,
	}, nil
}

func (r *Repository) UserByToken(ctx context.Context, token string) (AuthenticatedUser, error) {
	var userID string
	var sessionID string

	err := r.pool.QueryRow(
		ctx,
		`
		SELECT
			u.id::text,
			s.id::text
		FROM auth_sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1
		  AND s.revoked_at IS NULL
		  AND s.expires_at > now()
		  AND u.status = 'active'
		`,
		TokenHash(token),
	).Scan(&userID, &sessionID)
	if err != nil {
		return AuthenticatedUser{}, err
	}

	_, _ = r.pool.Exec(
		ctx,
		`
		UPDATE auth_sessions
		SET last_used_at = now()
		WHERE id = $1
		`,
		sessionID,
	)

	profile, err := r.ProfileByID(ctx, userID)
	if err != nil {
		return AuthenticatedUser{}, err
	}

	return AuthenticatedUser{
		UserProfile: profile,
		SessionID:   sessionID,
	}, nil
}

func (r *Repository) RevokeSession(ctx context.Context, sessionID string) error {
	_, err := r.pool.Exec(
		ctx,
		`
		UPDATE auth_sessions
		SET revoked_at = now()
		WHERE id = $1
		`,
		sessionID,
	)

	return err
}

func (r *Repository) ProfileByID(ctx context.Context, id string) (UserProfile, error) {
	var u UserProfile

	err := r.pool.QueryRow(
		ctx,
		`
		SELECT
			id::text,
			email,
			COALESCE(username, ''),
			COALESCE(display_name, ''),
			user_type,
			status
		FROM users
		WHERE id = $1
		`,
		id,
	).Scan(
		&u.ID,
		&u.Email,
		&u.Username,
		&u.DisplayName,
		&u.UserType,
		&u.Status,
	)
	if err != nil {
		return u, err
	}

	u.DisplayNameLegacy = u.DisplayName

	u.Roles, err = r.codes(
		ctx,
		`
		SELECT r.code
		FROM roles r
		JOIN user_roles ur ON ur.role_id = r.id
		WHERE ur.user_id = $1
		ORDER BY r.code
		`,
		id,
	)
	if err != nil {
		return u, err
	}

	u.Permissions, err = r.codes(
		ctx,
		`
		SELECT DISTINCT p.code
		FROM permissions p
		JOIN role_permissions rp ON rp.permission_id = p.id
		JOIN user_roles ur ON ur.role_id = rp.role_id
		WHERE ur.user_id = $1
		ORDER BY p.code
		`,
		id,
	)

	return u, err
}

func (r *Repository) codes(ctx context.Context, sql string, id string) ([]string, error) {
	rows, err := r.pool.Query(ctx, sql, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	codes := []string{}

	for rows.Next() {
		var c string

		if err := rows.Scan(&c); err != nil {
			return nil, err
		}

		codes = append(codes, c)
	}

	return codes, rows.Err()
}

func randomToken() (string, error) {
	b := make([]byte, 32)

	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}

func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func without(in []string, remove ...string) []string {
	m := map[string]bool{}

	for _, r := range remove {
		m[r] = true
	}

	out := []string{}

	for _, v := range in {
		if !m[v] {
			out = append(out, v)
		}
	}

	return out
}

func (r *Repository) RoleID(ctx context.Context, tx pgx.Tx, code string) (string, error) {
	var id string

	err := tx.QueryRow(
		ctx,
		`
		SELECT id::text
		FROM roles
		WHERE code = $1
		`,
		strings.TrimSpace(code),
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("unknown role %q", code)
	}

	return id, nil
}
