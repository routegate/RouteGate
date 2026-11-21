package recoveryenroll

import (
	"errors"
	"testing"
)

// Shared immutable wire vector: the Agent tests independently compute
// precisely this SHA over the same seven canonical JSON fields.
const offlineGenesisGoldenV1 = "150e39fa9bc14b73ad218d26cfdc26a9a596d3ae9d35a784aa63bb46f4ca620b"

func offlineGenesisFixtureV1() Preparation {
	return Preparation{
		ID: "11111111-1111-4111-8111-111111111111",
		AgentID: "33333333-3333-4333-8333-333333333333",
		ServerID: "22222222-2222-4222-8222-222222222222",
		AgentCredentialGeneration: 1,
		Phase: PhasePreparedUnattested,
	}
}

func TestRuntimeRecoveryEnrollmentGenesisExpectationV1GoldenWireContract(t *testing.T) {
	got, err := OfflineGenesisDigestUnattestedV1(offlineGenesisFixtureV1())
	if err != nil || got != offlineGenesisGoldenV1 {
		t.Fatalf("Manager genesis digest drift: got %q, want %q, err=%v", got, offlineGenesisGoldenV1, err)
	}
}

func TestRuntimeRecoveryEnrollmentGenesisExpectationV1IdentityAndGenerationChangesDigest(t *testing.T) {
	base := offlineGenesisFixtureV1()
	original, err := OfflineGenesisDigestUnattestedV1(base)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Preparation){
		"preparation": func(v *Preparation) { v.ID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" },
		"agent": func(v *Preparation) { v.AgentID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" },
		"server": func(v *Preparation) { v.ServerID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" },
		"generation": func(v *Preparation) { v.AgentCredentialGeneration++ },
	} {
		t.Run(name, func(t *testing.T) {
			modified := base
			change(&modified)
			got, err := OfflineGenesisDigestUnattestedV1(modified)
			if err != nil || got == original {
				t.Fatalf("Manager expectation failed to bind %s: %q %v", name, got, err)
			}
		})
	}
}

func TestRuntimeRecoveryEnrollmentGenesisExpectationV1RefusesInvalidTuple(t *testing.T) {
	base := offlineGenesisFixtureV1()
	for name, change := range map[string]func(*Preparation){
		"missing_preparation": func(v *Preparation) { v.ID = "" },
		"uppercase_preparation": func(v *Preparation) { v.ID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA" },
		"invalid_agent": func(v *Preparation) { v.AgentID = "not-a-uuid" },
		"missing_server": func(v *Preparation) { v.ServerID = "" },
		"zero_generation": func(v *Preparation) { v.AgentCredentialGeneration = 0 },
		"enrolled_claim": func(v *Preparation) { v.Phase = "enrolled" },
		"empty_phase": func(v *Preparation) { v.Phase = "" },
	} {
		t.Run(name, func(t *testing.T) {
			modified := base
			change(&modified)
			digest, err := OfflineGenesisDigestUnattestedV1(modified)
			if digest != "" || !errors.Is(err, ErrOfflineGenesisExpectationRefusedV1) {
				t.Fatalf("invalid Manager tuple returned digest=%q err=%v", digest, err)
			}
		})
	}
}
