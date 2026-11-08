package revocations

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestCredentialRemovalRecoveryDecisionSigningParity(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/removal-recovery-decision-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var decision RemovalRecoveryDecision
	if err := json.Unmarshal(data, &decision); err != nil {
		t.Fatal(err)
	}
	message, err := decision.SigningBytes()
	expected := append([]byte("routegate/v1/approve-held-vless-removal-reconciliation\x00"), bytes.TrimSpace(data)...)
	if err != nil || !bytes.Equal(message, expected) {
		t.Fatal("recovery approval signature contract drift", err)
	}
}
