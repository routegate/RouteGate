// Package revocations owns the internal, non-dispatching revocation preparation.
package revocations

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const TaskKindCredentialRemoval = "vless_credential_removal_v1"
const removalMaxConfigBytes = 8 << 20

var ErrCredentialRemoval = errors.New("credential removal safety check failed")

// CredentialRemovalRequest is delivered only by a separately authorized,
// reserved Manager operation. VLESSUUID is secret-bearing request material;
// neither the request nor the task may be logged or echoed in a response.
type CredentialRemovalRequest struct {
	SchemaVersion                    int    `json:"schemaVersion"`
	OperationID                      string `json:"operationId"`
	AccountID                        string `json:"accountId"`
	BaselineVersionID                string `json:"baselineVersionId"`
	BaselineRuntimeHash              string `json:"baselineRuntimeHash"`
	CandidateRuntimeHash             string `json:"candidateRuntimeHash"`
	VLESSUUID                        string `json:"vlessUuid"`
	SharedProcessRestartAcknowledged bool   `json:"sharedProcessRestartAcknowledged"`
}

// RuntimeGeneration is observed from the locally configured service, never
// supplied by the Manager. InvocationID distinguishes PID reuse and restarts.
type RuntimeGeneration struct {
	InvocationID     string `json:"invocationId"`
	PID              int    `json:"pid"`
	StartedMonotonic string `json:"startedMonotonic"`
}

// CredentialRemovalReport contains only bounded identifiers, measured hashes
// and fixed outcomes. It contains no config, command output, path or UUID.
type CredentialRemovalReport struct {
	SchemaVersion       int               `json:"schemaVersion"`
	JobID               string            `json:"jobId"`
	OperationID         string            `json:"operationId"`
	ServerID            string            `json:"serverId"`
	AgentID             string            `json:"agentId"`
	AccountID           string            `json:"accountId"`
	BaselineVersionID   string            `json:"baselineVersionId"`
	ConfigVersionID     string            `json:"configVersionId"`
	ConfigHash          string            `json:"configHash"`
	BaselineRuntimeHash string            `json:"baselineRuntimeHash"`
	ActiveRuntimeHash   string            `json:"activeRuntimeHash,omitempty"`
	Before              RuntimeGeneration `json:"before"`
	After               RuntimeGeneration `json:"after"`
	ListenerPort        int               `json:"listenerPort,omitempty"`
	State               string            `json:"state"`
	Code                string            `json:"code,omitempty"`
	SessionEffect       string            `json:"sessionEffect,omitempty"`
}

func validRemovalHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func normalizedRemovalUUID(value string) (string, bool) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return "", false
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return strings.ToLower(value), err == nil && len(raw) == 16
}

// canonicalRemovalJSON sorts object keys, preserves array order and JSON
// number spelling, and rejects duplicate keys, trailing input and non-objects.
// This versioned digest is of the runtime JSON, not the Manager envelope hash.
func canonicalRemovalJSON(data []byte) (map[string]any, []byte, string, error) {
	if len(data) == 0 || len(data) > removalMaxConfigBytes || !utf8.Valid(data) {
		return nil, nil, "", ErrCredentialRemoval
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 128 {
			return nil, ErrCredentialRemoval
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, ErrCredentialRemoval
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				object := map[string]any{}
				for decoder.More() {
					keyToken, err := decoder.Token()
					if err != nil {
						return nil, ErrCredentialRemoval
					}
					key, ok := keyToken.(string)
					if !ok {
						return nil, ErrCredentialRemoval
					}
					if _, duplicate := object[key]; duplicate {
						return nil, ErrCredentialRemoval
					}
					value, err := read(depth + 1)
					if err != nil {
						return nil, err
					}
					object[key] = value
				}
				if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
					return nil, ErrCredentialRemoval
				}
				return object, nil
			case '[':
				array := []any{}
				for decoder.More() {
					value, err := read(depth + 1)
					if err != nil {
						return nil, err
					}
					array = append(array, value)
				}
				if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
					return nil, ErrCredentialRemoval
				}
				return array, nil
			default:
				return nil, ErrCredentialRemoval
			}
		}
		return token, nil
	}
	value, err := read(0)
	if err != nil {
		return nil, nil, "", err
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, nil, "", ErrCredentialRemoval
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, nil, "", ErrCredentialRemoval
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, nil, "", ErrCredentialRemoval
	}
	sum := sha256.Sum256(canonical)
	return object, canonical, hex.EncodeToString(sum[:]), nil
}

// RuntimeDigest implements the Agent v1 canonical runtime digest. Shared golden
// vectors in testdata bind the independently built Go modules to one protocol.
func RuntimeDigest(data []byte) (string, error) {
	_, _, hash, err := canonicalRemovalJSON(data)
	return hash, err
}
