package revocations

import (
	"encoding/json"
	"os"
	"testing"
)

func TestCredentialRemovalV1GoldenDigest(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/credential-removal-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct{ Input, Canonical, SHA256 string }
	if err = json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		_, canonical, hash, err := canonicalRemovalJSON([]byte(v.Input))
		if err != nil || string(canonical) != v.Canonical || hash != v.SHA256 {
			t.Fatal("v1 runtime digest drift", err)
		}
	}
}
