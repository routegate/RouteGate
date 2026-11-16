package tasks

import "testing"

// Independent Agent-side golden vector for the Manager's exact JSON field
// order, domain-separated SHA-256 and preparation identity. A matching
// digest says NOTHING about host provenance or enrollment authorization.
func TestRuntimeRecoveryEnrollmentGenesisOfflineV1WireGoldenDigest(t *testing.T) {
	const want = "150e39fa9bc14b73ad218d26cfdc26a9a596d3ae9d35a784aa63bb46f4ca620b"
	got, err := recoveryEnrollmentGenesisDigestV1(recoveryEnrollmentGenesisFixtureV1())
	if err != nil || got != want {
		t.Fatalf("Agent/Manager offline genesis contract drift: got %q, want %q, err=%v", got, want, err)
	}
}
