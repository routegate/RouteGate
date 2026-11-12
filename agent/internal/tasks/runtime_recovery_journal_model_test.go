package tasks

import (
	"errors"
	"strings"
	"testing"
)

func syntheticRecoveryJournalV1(t *testing.T) []recoveryJournalEventV1 {
	t.Helper()
	phases := []recoveryJournalPhaseV1{
		recoveryJournalIntentPreparedV1,
		recoveryJournalMutationUncertainV1,
		recoveryJournalResultPersistedV1,
		recoveryJournalReceiptAcknowledgedV1,
		recoveryJournalReviewRequestedV1,
	}
	records := make([]recoveryJournalEventV1, len(phases))
	previous := strings.Repeat("0", 64)
	for i, phase := range phases {
		receipt := ""
		if i == 2 {
			receipt = strings.Repeat("b", 64)
		}
		if i >= 3 {
			receipt = strings.Repeat("c", 64)
		}
		records[i] = recoveryJournalEventV1{
			SchemaVersion: 1, Sequence: uint64(i + 1), Phase: phase,
			AttemptID: "55555555-5555-4555-8555-555555555555",
			AttemptEpoch: 17,
			TaskID: "44444444-4444-4444-8444-444444444444",
			AgentID: "33333333-3333-4333-8333-333333333333",
			ServerID: "22222222-2222-4222-8222-222222222222",
			AgentCredentialGeneration: 4,
			MarkerSHA256: strings.Repeat("a", 64),
			ReceiptSHA256: receipt,
			PreviousSHA256: previous,
		}
		digest, err := recoveryJournalEventDigestV1(records[i])
		if err != nil {
			t.Fatal(err)
		}
		previous = digest
	}
	return records
}

// All crash cuts are readable as historical prefixes, and none can ever
// authorize a resumed mutation. A valid chain is NOT a terminal record.
func TestRuntimeRecoveryJournalV1EveryCrashCutRemainsUnattested(t *testing.T) {
	records := syntheticRecoveryJournalV1(t)
	for count := 1; count <= len(records); count++ {
		t.Run(string(records[count-1].Phase), func(t *testing.T) {
			review, err := reviewRecoveryJournalChainV1(records[:count])
			if err != nil {
				t.Fatal(err)
			}
			if !review.Unattested || review.EventsSeen != count ||
				review.LastPhase != records[count-1].Phase ||
				!validRemovalHash(review.HeadSHA256) {
				t.Fatalf("journal incorrectly authorized recovery: %+v", review)
			}
		})
	}
}

// The model treats lack of a durable journal as unresolved, even when an
// observer says that both old intent and result files have disappeared.
func TestRuntimeRecoveryJournalV1AbsentOrDamagedCannotBeAccepted(t *testing.T) {
	for name, records := range map[string][]recoveryJournalEventV1{
		"no_journal": nil,
		"empty_journal": {},
		"trailing_replay": append(syntheticRecoveryJournalV1(t), syntheticRecoveryJournalV1(t)[4]),
	} {
		t.Run(name, func(t *testing.T) {
			review, err := reviewRecoveryJournalChainV1(records)
			if !errors.Is(err, errRecoveryJournalUntrustedV1) ||
				review != (recoveryJournalIntegrityObservationV1{}) {
				t.Fatalf("absent/corrupt journal became trusted: %+v %v", review, err)
			}
		})
	}
}

func TestRuntimeRecoveryJournalV1RejectsReorderReplayAndIdentityDrift(t *testing.T) {
	mutations := map[string]func([]recoveryJournalEventV1){
		"version": func(e []recoveryJournalEventV1) { e[0].SchemaVersion = 2 },
		"missing_head": func(e []recoveryJournalEventV1) { e[0].PreviousSHA256 = "" },
		"wrong_head": func(e []recoveryJournalEventV1) { e[0].PreviousSHA256 = strings.Repeat("e", 64) },
		"out_of_order": func(e []recoveryJournalEventV1) { e[1], e[2] = e[2], e[1] },
		"sequence_reused": func(e []recoveryJournalEventV1) { e[2].Sequence = 2 },
		"wrong_phase": func(e []recoveryJournalEventV1) { e[1].Phase = "release_committed" },
		"wrong_attempt": func(e []recoveryJournalEventV1) { e[2].AttemptID = e[0].TaskID },
		"wrong_epoch": func(e []recoveryJournalEventV1) { e[2].AttemptEpoch++ },
		"wrong_task": func(e []recoveryJournalEventV1) { e[2].TaskID = e[0].AgentID },
		"wrong_agent": func(e []recoveryJournalEventV1) { e[2].AgentID = e[0].ServerID },
		"wrong_server": func(e []recoveryJournalEventV1) { e[2].ServerID = e[0].AgentID },
		"rotated_bearer": func(e []recoveryJournalEventV1) { e[2].AgentCredentialGeneration++ },
		"marker_changed": func(e []recoveryJournalEventV1) { e[2].MarkerSHA256 = strings.Repeat("d", 64) },
		"bad_marker": func(e []recoveryJournalEventV1) { e[2].MarkerSHA256 = "not-a-hash" },
		"premature_receipt": func(e []recoveryJournalEventV1) { e[1].ReceiptSHA256 = strings.Repeat("b", 64) },
		"missing_receipt": func(e []recoveryJournalEventV1) { e[2].ReceiptSHA256 = "" },
		"missing_ack_rewrite": func(e []recoveryJournalEventV1) { e[3].ReceiptSHA256 = e[2].ReceiptSHA256 },
		"receipt_changed_after_ack": func(e []recoveryJournalEventV1) { e[4].ReceiptSHA256 = strings.Repeat("f", 64) },
		"broken_chain": func(e []recoveryJournalEventV1) { e[4].PreviousSHA256 = strings.Repeat("f", 64) },
		"truncated_identity": func(e []recoveryJournalEventV1) { e[0].AttemptID = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			events := syntheticRecoveryJournalV1(t)
			mutate(events)
			review, err := reviewRecoveryJournalChainV1(events)
			if !errors.Is(err, errRecoveryJournalUntrustedV1) ||
				review != (recoveryJournalIntegrityObservationV1{}) {
				t.Fatalf("tampered local journal accepted: %+v %v", review, err)
			}
		})
	}
}

// A privileged attacker could rewrite every record and recompute all hashes.
// The chain may then be *internally consistent*, but it remains UNATTESTED.
// This regression prevents future code from mistaking SHA-256 for a trusted
// Manager checkpoint or signed host attestation.
func TestRuntimeRecoveryJournalV1ForgedConsistentChainIsStillUnattested(t *testing.T) {
	events := syntheticRecoveryJournalV1(t)
	previous := strings.Repeat("0", 64)
	for i := range events {
		events[i].MarkerSHA256 = strings.Repeat("d", 64)
		events[i].PreviousSHA256 = previous
		digest, err := recoveryJournalEventDigestV1(events[i])
		if err != nil {
			t.Fatal(err)
		}
		previous = digest
	}
	review, err := reviewRecoveryJournalChainV1(events)
	if err != nil {
		t.Fatal(err)
	}
	if !review.Unattested || review.EventsSeen != len(events) {
		t.Fatalf("rewritten hash chain got accidental authority: %+v", review)
	}
}
