package tasks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

// This package-private journal MODEL has no disk writer, HTTP route, Agent
// Runner hook or mutation/unlock action. A self-consistent local hash chain
// detects accidental edits, NOT a compromised Agent or privileged attacker.
// No result from this model is authorization to clear a runtime fence.
var errRecoveryJournalUntrustedV1 = errors.New("recovery journal requires independent review")

type recoveryJournalPhaseV1 string

const (
	recoveryJournalIntentPreparedV1     recoveryJournalPhaseV1 = "intent_prepared"
	recoveryJournalMutationUncertainV1  recoveryJournalPhaseV1 = "mutation_uncertain"
	recoveryJournalResultPersistedV1    recoveryJournalPhaseV1 = "result_persisted"
	recoveryJournalReceiptAcknowledgedV1 recoveryJournalPhaseV1 = "receipt_acknowledged"
	recoveryJournalReviewRequestedV1    recoveryJournalPhaseV1 = "review_requested"
)

const recoveryJournalHashDomainV1 = "routegate/rg140/local-recovery-journal/v1"

// recoveryJournalEventV1 describes a possible future append-only journal
// record. The marker and receipt hashes refer to EXACT file bytes. A receipt
// must change digest when delivery is acknowledged and rewritten. Identifiers
// and credential generation bind every event to one immutable attempt.
type recoveryJournalEventV1 struct {
	SchemaVersion             int                    `json:"schemaVersion"`
	Sequence                  uint64                 `json:"sequence"`
	Phase                     recoveryJournalPhaseV1 `json:"phase"`
	AttemptID                 string                 `json:"attemptId"`
	AttemptEpoch              int64                  `json:"attemptEpoch"`
	TaskID                    string                 `json:"taskId"`
	AgentID                   string                 `json:"agentId"`
	ServerID                  string                 `json:"serverId"`
	AgentCredentialGeneration int64                  `json:"agentCredentialGeneration"`
	MarkerSHA256              string                 `json:"markerSha256"`
	ReceiptSHA256             string                 `json:"receiptSha256"`
	PreviousSHA256            string                 `json:"previousSha256"`
}

// recoveryJournalIntegrityObservationV1 deliberately contains NO CanResume,
// Approved, Attested or CanUnlock flag. Even a perfectly valid local chain is
// only a syntactic observation awaiting independent authorization/provenance.
type recoveryJournalIntegrityObservationV1 struct {
	EventsSeen int
	LastPhase  recoveryJournalPhaseV1
	HeadSHA256 string
	Unattested bool
}

func recoveryJournalPhaseOrdinalV1(p recoveryJournalPhaseV1) int {
	switch p {
	case recoveryJournalIntentPreparedV1:
		return 0
	case recoveryJournalMutationUncertainV1:
		return 1
	case recoveryJournalResultPersistedV1:
		return 2
	case recoveryJournalReceiptAcknowledgedV1:
		return 3
	case recoveryJournalReviewRequestedV1:
		return 4
	default:
		return -1
	}
}

// recoveryJournalEventDigestV1 is intentionally NOT a MAC or host
// attestation. The domain prefix prevents mixing the local journal with
// Agent-bearer proofs or full HTTP response HMACs.
func recoveryJournalEventDigestV1(event recoveryJournalEventV1) (string, error) {
	encoded, err := json.Marshal(event)
	if err != nil {
		return "", errRecoveryJournalUntrustedV1
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(recoveryJournalHashDomainV1))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(encoded)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// reviewRecoveryJournalChainV1 checks only internal consistency, never the
// provenance of the first event or completeness of the tail. An attacker able
// to rewrite every event can recompute every digest; a truncated VALID prefix
// looks like a real crash. Both require an independently anchored checkpoint.
// A missing/corrupt chain yields no observation; a valid prefix is UNATTESTED.
func reviewRecoveryJournalChainV1(events []recoveryJournalEventV1) (recoveryJournalIntegrityObservationV1, error) {
	deny := func() (recoveryJournalIntegrityObservationV1, error) {
		return recoveryJournalIntegrityObservationV1{}, errRecoveryJournalUntrustedV1
	}
	if len(events) == 0 || len(events) > 5 {
		return deny()
	}
	previous := strings.Repeat("0", 64)
	var original recoveryJournalEventV1
	for i, event := range events {
		if event.SchemaVersion != 1 ||
			event.Sequence != uint64(i+1) ||
			recoveryJournalPhaseOrdinalV1(event.Phase) != i ||
			!canonicalTaskIDPattern.MatchString(event.AttemptID) ||
			!canonicalTaskIDPattern.MatchString(event.TaskID) ||
			!canonicalTaskIDPattern.MatchString(event.AgentID) ||
			!canonicalTaskIDPattern.MatchString(event.ServerID) ||
			event.AttemptEpoch <= 0 || event.AgentCredentialGeneration <= 0 ||
			!validRemovalHash(event.MarkerSHA256) ||
			event.PreviousSHA256 != previous {
			return deny()
		}
		switch i {
		case 0, 1:
			// No acknowledged result bytes exist at these phases.
			if event.ReceiptSHA256 != "" {
				return deny()
			}
		default:
			if !validRemovalHash(event.ReceiptSHA256) {
				return deny()
			}
		}
		if i == 0 {
			original = event
		} else {
			if event.AttemptID != original.AttemptID ||
				event.AttemptEpoch != original.AttemptEpoch ||
				event.TaskID != original.TaskID ||
				event.AgentID != original.AgentID ||
				event.ServerID != original.ServerID ||
				event.AgentCredentialGeneration != original.AgentCredentialGeneration ||
				event.MarkerSHA256 != original.MarkerSHA256 {
				return deny()
			}
		}
		if i == 3 && event.ReceiptSHA256 == events[2].ReceiptSHA256 {
			// The ACK rewrites persisted receipt bytes in the current Agent.
			return deny()
		}
		if i == 4 && event.ReceiptSHA256 != events[3].ReceiptSHA256 {
			return deny()
		}
		digest, err := recoveryJournalEventDigestV1(event)
		if err != nil {
			return deny()
		}
		previous = digest
	}
	return recoveryJournalIntegrityObservationV1{
		EventsSeen: len(events),
		LastPhase: events[len(events)-1].Phase,
		HeadSHA256: previous,
		Unattested: true,
	}, nil
}
