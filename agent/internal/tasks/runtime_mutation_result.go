package tasks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
)

const runtimeResultFile = "mutation-result.json"
const runtimeResultLimit = 1024 * 1024

// RuntimeResultBinding prevents replay to a different Manager or Agent identity.
// Tokens and rendered configurations are never stored in this binding.
type RuntimeResultBinding struct {
	ManagerURL string `json:"managerUrl"`
	AgentID    string `json:"agentId"`
	ServerID   string `json:"serverId"`
}

type runtimeResultReceipt struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Binding       RuntimeResultBinding `json:"binding"`
	MarkerHash    string               `json:"markerHash"`
	TaskID        string               `json:"taskId"`
	Envelope      json.RawMessage      `json:"envelope"`
	Acknowledged  bool                 `json:"acknowledged"`
}

func markerDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validRuntimeResult(data []byte) bool {
	var result struct {
		Status       string `json:"status"`
		ErrorMessage string `json:"errorMessage"`
	}
	return len(data) > 0 && len(data) <= runtimeResultLimit/2 && json.Unmarshal(data, &result) == nil && result.Status == "succeeded" && result.ErrorMessage == ""
}

// SaveResult must run before delivery while the original mutation lease is held.
// A crash before this call leaves only intent, which cannot be replayed.
func (m *RuntimeMutation) SaveResult(binding RuntimeResultBinding, taskID string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lock == nil || m.completed || binding.ManagerURL == "" || !canonicalTaskIDPattern.MatchString(binding.AgentID) || !canonicalTaskIDPattern.MatchString(binding.ServerID) || !validRuntimeResult(data) {
		return ErrRuntimeMutationBlocked
	}
	var marker struct {
		TaskID string `json:"taskId"`
	}
	if json.Unmarshal(m.marker, &marker) != nil || marker.TaskID != taskID {
		return ErrRuntimeMutationBlocked
	}
	current, err := readRemovalFile(m.path, 16384, true)
	if err != nil || !bytes.Equal(current, m.marker) || !runtimeMarkerAbsent(filepath.Join(filepath.Dir(m.path), "inflight.json")) {
		return ErrRuntimeMutationBlocked
	}
	path := filepath.Join(filepath.Dir(m.path), runtimeResultFile)
	receipt := runtimeResultReceipt{SchemaVersion: 1, Binding: binding, MarkerHash: markerDigest(m.marker), TaskID: taskID, Envelope: append(json.RawMessage(nil), data...)}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > runtimeResultLimit {
		return ErrRuntimeMutationBlocked
	}
	if !runtimeMarkerAbsent(path) {
		previous, readErr := readRemovalFile(path, runtimeResultLimit, true)
		if readErr != nil || !bytes.Equal(previous, encoded) {
			return ErrRuntimeMutationBlocked
		}
		m.result = append([]byte(nil), encoded...)
		return nil
	}
	if writeRemovalFile(path, encoded) != nil {
		return ErrRuntimeMutationBlocked
	}
	m.result = append([]byte(nil), encoded...)
	return nil
}

// ReplayRuntimeResult takes the same exclusive lock as runtime mutations. It
// delivers only a saved success, never invokes runtime code and never clears the
// mutation marker. Historical delivery is not proof of current runtime state.
// Acknowledged receipts remain for future authenticated reconciliation.
func ReplayRuntimeResult(dir string, binding RuntimeResultBinding, deliver func(string, []byte) error) error {
	if !RuntimeMutationStatePresent(dir) {
		return nil
	}
	lock, err := lockRuntimeMutation(dir)
	if err != nil {
		return ErrRuntimeMutationBlocked
	}
	defer lock.Close()
	path := filepath.Join(dir, runtimeResultFile)
	if runtimeMarkerAbsent(path) {
		return nil
	}
	data, err := readRemovalFile(path, runtimeResultLimit, true)
	if err != nil {
		return ErrRuntimeMutationBlocked
	}
	var receipt runtimeResultReceipt
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || !json.Valid(data) || receipt.SchemaVersion != 1 || receipt.Binding != binding || !canonicalTaskIDPattern.MatchString(receipt.TaskID) || !validRuntimeResult(receipt.Envelope) {
		return ErrRuntimeMutationBlocked
	}
	marker, err := readRemovalFile(filepath.Join(dir, "mutation-inflight.json"), 16384, true)
	var intent struct {
		SchemaVersion int    `json:"schemaVersion"`
		TaskID        string `json:"taskId"`
	}
	if err != nil || markerDigest(marker) != receipt.MarkerHash || json.Unmarshal(marker, &intent) != nil || intent.SchemaVersion != 1 || intent.TaskID != receipt.TaskID || !runtimeMarkerAbsent(filepath.Join(dir, "inflight.json")) {
		return ErrRuntimeMutationBlocked
	}
	if receipt.Acknowledged {
		return nil
	}
	if deliver(receipt.TaskID, append([]byte(nil), receipt.Envelope...)) != nil {
		return ErrRuntimeMutationBlocked
	}
	current, err := readRemovalFile(path, runtimeResultLimit, true)
	currentMarker, markerErr := readRemovalFile(filepath.Join(dir, "mutation-inflight.json"), 16384, true)
	if err != nil || markerErr != nil || !bytes.Equal(current, data) || !bytes.Equal(currentMarker, marker) || !runtimeMarkerAbsent(filepath.Join(dir, "inflight.json")) {
		return ErrRuntimeMutationBlocked
	}
	receipt.Acknowledged = true
	encoded, err := json.Marshal(receipt)
	if err != nil || writeRemovalFile(path, encoded) != nil {
		return ErrRuntimeMutationBlocked
	}
	return nil
}
