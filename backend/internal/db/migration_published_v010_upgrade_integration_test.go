package db

import (
	"context"
	"crypto/sha1" // Git's blob object identity for the tagged v0.1.0 source, not a security checksum.
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// v0.1.0 is the actual signed/annotated GitHub release tag from 2026-08-07,
// peeled to commit 407b08a9f357682ee3e70fe550a2e342017a33b5.
// GitHub's published tag tree has these *exact 21* migration blobs. At
// review time they match the same files already preserved in current main.
// Pinning each Git blob hash makes the test independent of git fetch/network
// while ensuring the legacy fixture remains bit-for-bit the released SQL.
//
// SHA-1 below is only the repository's Git blob identity format. It is NOT
// used to authenticate downloaded release artifacts or for runtime security.
var publishedV010MigrationBlobs = map[string]string{
	"000001_init.up.sql": "57d73cf92ba1446a95bf3a068e2e973ecf1b9902",
	"000002_agent_identity.up.sql": "7e3571d31773b30ba683a3b93f3917a2316a8f32",
	"000003_auth_foundation.up.sql": "ad7d21cdb5fe1cdaa463fa271988581d1d768b3a",
	"000003_config_apply_jobs.up.sql": "47ac093d5974f0ff7bd104718fd738710a4644cc",
	"000004_server_registry_agents.up.sql": "fdad3d7f843f7e8d425e943a3a40031e4e97e2c3",
	"000020_traffic_stats_limits.up.sql": "a1fb170f88e8a7db3e1eccebb1f5ba65846abb18",
	"000046_reality_keypair_management.up.sql": "bfbea144198e20bd079cbbadbaa2f5e991dedf40",
	"000060_routing_profiles_split_tunnel.up.sql": "cfde73bf02b5090babb05cd56290272ff01b0db1",
	"000063_routing_profiles_validation_hardening.up.sql": "120451c0af752bca1b8b650a0f662385492f0f45",
	"000074_traffic_limit_enforcement.up.sql": "fca088bb2f943ec39e8d4575dc8cad5779b7e131",
	"000097_audit_events.up.sql": "39d2132426107d090a78c9a188d45d62cf44e971",
	"000099_vpn_accounts_foundation.up.sql": "d2c63b28c24704ad9a6bf232f425cf7567ee1901",
	"000100_vpn_subscription_tokens.up.sql": "b52824d86004994df2d60c959972056bf606de8d",
	"000101_initial_setup_tokens.up.sql": "9a1846d14aecab0af05a88c41d6e6e22f75a5def",
	"000101_vless_reality_credentials.up.sql": "1213ce22e8481e82ede0652aaf5c90214adb5a2e",
	"000102_agent_protocol_version.up.sql": "7f7e4a47a02b2095103a08a350ca30ec87560e0f",
	"000102_config_apply_success_marks_version_applied.up.sql": "ad7e49ee59c418948c24ba59cff2b2417ace8323",
	"000102_vpn_client_profiles.up.sql": "12e0a8136fb97971f14c66c74407082cdd43adab",
	"000103_agent_operation_jobs.up.sql": "d02b19c40d452df68a788ebcf20c43e158058070",
	"000103_protocol_settings_timestamp.up.sql": "8b4c04b84a8159b2220e9e1014d52cb91135de3e",
	"000104_vpn_core_installation_jobs.up.sql": "d617e7d7e2921d1a45e40300e2a7e443cd4461db",
}

func testGitBlobIdentity(contents []byte) string {
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "blob %d\\x00", len(contents))
	_, _ = h.Write(contents)
	return hex.EncodeToString(h.Sum(nil))
}

func TestPublishedV010SchemaUpgradesOnPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("ROUTEGATE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ROUTEGATE_TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := Connect(ctx, databaseURL, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// This test resets only the disposable CI database, never an externally
	// provided real host. The same environment gate protects other E2E tests.
	resetPublicSchema(t, ctx, pool)
	releaseFixture := t.TempDir()
	if len(publishedV010MigrationBlobs) != 21 {
		t.Fatalf("v0.1.0 release fixture changed from its 21 archived SQL files")
	}
	for name, expectedBlob := range publishedV010MigrationBlobs {
		actual, err := os.ReadFile(filepath.Join("../../migrations", name))
		if err != nil {
			t.Fatalf("missing original released migration %s: %v", name, err)
		}
		if got := testGitBlobIdentity(actual); got != expectedBlob {
			t.Fatalf("published v0.1.0 migration %s changed: Git blob=%s, release=%s", name, got, expectedBlob)
		}
		if err := os.WriteFile(filepath.Join(releaseFixture, name), actual, 0600); err != nil {
			t.Fatal(err)
		}
	}

	if err := Migrate(ctx, pool, releaseFixture, logger); err != nil {
		t.Fatalf("apply exact published v0.1.0 migration set: %v", err)
	}
	var firstCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&firstCount); err != nil {
		t.Fatal(err)
	}
	if firstCount != 21 {
		t.Fatalf("published release applied %d migrations instead of 21", firstCount)
	}

	// Simulate pre-existing v0.1.0 state that must remain valid after *all*
	// later migrations, including revised constraints and account-device scope.
	var serverID, accountID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO servers (name, status)
		VALUES ('published-v010-existing-node', 'active')
		RETURNING id::text
	`).Scan(&serverID); err != nil {
		t.Fatalf("seed released-schema server: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO vpn_accounts (username, protocol, display_name, status, server_id)
		VALUES ('published-v010-existing-account', 'sing-box', 'Existing v0.1.0 account', 'active', $1::uuid)
		RETURNING id::text
	`, serverID).Scan(&accountID); err != nil {
		t.Fatalf("seed released-schema account: %v", err)
	}

	var oldApplied time.Time
	if err := pool.QueryRow(ctx, `
		SELECT applied_at FROM schema_migrations
		WHERE version = '000104_vpn_core_installation_jobs'
	`).Scan(&oldApplied); err != nil {
		t.Fatal(err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if err := Migrate(ctx, pool, "../../migrations", logger); err != nil {
			t.Fatalf("upgrade published v0.1.0 catalog, pass %d: %v", attempt, err)
		}
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 85 {
			t.Fatalf("upgrade pass %d has %d applied migrations, want 85", attempt, count)
		}
		var name, accountName, accountStatus string
		if err := pool.QueryRow(ctx, "SELECT name FROM servers WHERE id = $1::uuid", serverID).Scan(&name); err != nil {
			t.Fatalf("released server lost on upgrade pass %d: %v", attempt, err)
		}
		if err := pool.QueryRow(ctx, `
			SELECT display_name, status FROM vpn_accounts WHERE id = $1::uuid
		`, accountID).Scan(&accountName, &accountStatus); err != nil {
			t.Fatalf("released VPN account lost on upgrade pass %d: %v", attempt, err)
		}
		if name != "published-v010-existing-node" ||
			accountName != "Existing v0.1.0 account" || accountStatus != "active" {
			t.Fatalf("v0.1.0 data changed on pass %d: node=%q account=%q status=%q", attempt, name, accountName, accountStatus)
		}
		var afterApplied time.Time
		if err := pool.QueryRow(ctx, `
			SELECT applied_at FROM schema_migrations
			WHERE version = '000104_vpn_core_installation_jobs'
		`).Scan(&afterApplied); err != nil {
			t.Fatal(err)
		}
		if !afterApplied.Equal(oldApplied) {
			t.Fatalf("published migration history changed on pass %d", attempt)
		}
	}

	version, err := NewSchemaVersionRepository(pool).AppliedSchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != "000160_traffic_report_checkpoints" {
		t.Fatalf("post-upgrade version=%q, want 000160_traffic_report_checkpoints", version)
	}
}
