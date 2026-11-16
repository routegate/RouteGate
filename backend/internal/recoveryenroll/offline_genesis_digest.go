package recoveryenroll

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// OFFLINE contract shared by independently implemented Manager/Agent tests.
// This is only a deterministic expectation over Manager-origin preparation
// fields; a digest is not a certificate, authenticated challenge, Agent host
// identity, enrolled state, dispatch right, or mutation/recovery permission.
const offlineGenesisDomainV1 = "routegate/rg140/offline-enrollment-genesis/v1"

var ErrOfflineGenesisExpectationRefusedV1 = errors.New("offline genesis expectation refused")

type offlineGenesisV1 struct {
	SchemaVersion             int    `json:"schemaVersion"`
	ManagerPreparationID      string `json:"managerPreparationId"`
	AgentID                   string `json:"agentId"`
	ServerID                  string `json:"serverId"`
	AgentCredentialGeneration int64  `json:"agentCredentialGeneration"`
	ManagerPreparationPhase   string `json:"managerPreparationPhase"`
	NextJournalSequence       int    `json:"nextJournalSequence"`
}

// OfflineGenesisDigestUnattestedV1 is deliberately NOT a method of the live
// Manager dispatcher. Its input must be obtained by a future trusted Manager
// snapshot reader, not accepted from an Agent or HTTP request.
func OfflineGenesisDigestUnattestedV1(p Preparation) (string, error) {
	if !enrollmentCanonicalUUID.MatchString(p.ID) ||
		!enrollmentCanonicalUUID.MatchString(p.AgentID) ||
		!enrollmentCanonicalUUID.MatchString(p.ServerID) ||
		p.AgentCredentialGeneration <= 0 ||
		p.Phase != PhasePreparedUnattested {
		return "", ErrOfflineGenesisExpectationRefusedV1
	}
	v := offlineGenesisV1{
		SchemaVersion: 1,
		ManagerPreparationID: p.ID,
		AgentID: p.AgentID,
		ServerID: p.ServerID,
		AgentCredentialGeneration: p.AgentCredentialGeneration,
		ManagerPreparationPhase: PhasePreparedUnattested,
		NextJournalSequence: 1,
	}
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 || len(raw) > 4096 {
		return "", ErrOfflineGenesisExpectationRefusedV1
	}
	h := sha256.New()
	_, _ = h.Write([]byte(offlineGenesisDomainV1))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil)), nil
}
