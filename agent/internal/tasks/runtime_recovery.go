package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
)

// RuntimeRecoveryWitness is a historical local observation, not permission to
// unlock. Only the locally configured sing-box adapter may collect this proof.
type RuntimeRecoveryWitness struct {
	SchemaVersion      int               `json:"schemaVersion"`
	RuntimeHash        string            `json:"runtimeHash"`
	Generation         RuntimeGeneration `json:"generation"`
	PreviousGeneration RuntimeGeneration `json:"previousGeneration"`
	ListenerPort       int               `json:"listenerPort"`
}

func SupportsRuntimeRecovery(adapter VPNCoreAdapter) bool {
	if adapter == nil {
		return false
	}
	if _, ok := adapter.(removalGenerationObserver); !ok {
		return false
	}
	d := adapter.Descriptor()
	return d.Core == "sing-box" && d.Protocol == "vless"
}

func validRuntimeWitness(w RuntimeRecoveryWitness) bool {
	return w.SchemaVersion == 1 && validRemovalHash(w.RuntimeHash) && validRemovalGeneration(w.Generation) && validRemovalGeneration(w.PreviousGeneration) && w.PreviousGeneration.InvocationID != w.Generation.InvocationID && w.ListenerPort > 0 && w.ListenerPort <= 65535
}

// RuntimeRecoveryTargetHash binds a config-apply checkpoint to the actual task's
// sing-box candidate, rather than whichever file happened to be present later.
func RuntimeRecoveryTargetHash(task ConfigTask) (string, error) {
	envelope, _, _, err := canonicalRemovalJSON(task.RenderedConfig)
	if err != nil || envelope["schemaVersion"] != "routegate.config.v1" {
		return "", ErrRuntimeMutationBlocked
	}
	config, ok := envelope["singBox"].(map[string]any)
	if !ok {
		return "", ErrRuntimeMutationBlocked
	}
	data, err := json.Marshal(config)
	if err != nil {
		return "", ErrRuntimeMutationBlocked
	}
	_, _, hash, err := canonicalRemovalJSON(data)
	if err != nil {
		return "", ErrRuntimeMutationBlocked
	}
	return hash, nil
}

// CaptureRuntimeRecoveryWitness performs read-only, bracketed observations.
// ObserveGeneration on the production adapter verifies /proc executable/config
// source and systemd invocation. Listener/enablement alone are insufficient.
// The caller MUST hold the shared mutation lock throughout this observation.
func CaptureRuntimeRecoveryWitness(ctx context.Context, activePath string, adapter VPNCoreAdapter) (RuntimeRecoveryWitness, error) {
	deny := func() (RuntimeRecoveryWitness, error) { return RuntimeRecoveryWitness{}, ErrRuntimeMutationBlocked }
	if ctx.Err() != nil || !filepath.IsAbs(activePath) || !SupportsRuntimeRecovery(adapter) {
		return deny()
	}
	observer := adapter.(removalGenerationObserver)
	before, err := observer.ObserveGeneration(ctx, activePath)
	if err != nil || !validRemovalGeneration(before) {
		return deny()
	}
	data, err := readRemovalFile(activePath, removalMaxConfigBytes, false)
	_, _, hash, hashErr := canonicalRemovalJSON(data)
	if err != nil || hashErr != nil {
		return deny()
	}
	if _, err := adapter.IsEnabled(ctx); err != nil {
		return deny()
	}
	listener, err := adapter.CheckHealth(ctx, activePath)
	if err != nil || listener.Port < 1 || listener.Port > 65535 {
		return deny()
	}
	current, err := readRemovalFile(activePath, removalMaxConfigBytes, false)
	_, _, currentHash, hashErr := canonicalRemovalJSON(current)
	after, generationErr := observer.ObserveGeneration(ctx, activePath)
	if ctx.Err() != nil || err != nil || hashErr != nil || generationErr != nil || hash != currentHash || before != after {
		return deny()
	}
	return RuntimeRecoveryWitness{SchemaVersion: 1, RuntimeHash: hash, Generation: after, ListenerPort: listener.Port}, nil
}

// RuntimeRecoveryObservation is evidence for a future authorized recovery
// operation. It contains no config, command output, credentials or local paths.
// ObserveRuntimeRecovery never deletes an intent or authorizes a new mutation.
type RuntimeRecoveryObservation struct {
	SchemaVersion int                    `json:"schemaVersion"`
	TaskID        string                 `json:"taskId"`
	AgentID       string                 `json:"agentId"`
	ServerID      string                 `json:"serverId"`
	MarkerHash    string                 `json:"markerHash"`
	ReceiptHash   string                 `json:"receiptHash"`
	EnvelopeHash  string                 `json:"envelopeHash"`
	Witness       RuntimeRecoveryWitness `json:"witness"`
}

func ObserveRuntimeRecovery(ctx context.Context, dir string, binding RuntimeResultBinding, taskID, activePath string, adapter VPNCoreAdapter) (RuntimeRecoveryObservation, error) {
	deny := func() (RuntimeRecoveryObservation, error) {
		return RuntimeRecoveryObservation{}, ErrRuntimeMutationBlocked
	}
	if !canonicalTaskIDPattern.MatchString(taskID) || !RuntimeMutationStatePresent(dir) {
		return deny()
	}
	lock, err := lockRuntimeMutation(dir)
	if err != nil {
		return deny()
	}
	defer lock.Close()
	markerPath := filepath.Join(dir, "mutation-inflight.json")
	receiptPath := filepath.Join(dir, runtimeResultFile)
	marker, err := readRemovalFile(markerPath, 16384, true)
	if err != nil || !runtimeMarkerAbsent(filepath.Join(dir, "inflight.json")) {
		return deny()
	}
	var intent struct {
		SchemaVersion int    `json:"schemaVersion"`
		TaskID        string `json:"taskId"`
		Kind          string `json:"kind"`
	}
	if json.Unmarshal(marker, &intent) != nil || intent.SchemaVersion != 1 || intent.TaskID != taskID || (intent.Kind != TaskKindConfigApply && intent.Kind != TaskKindVPNCoreService) {
		return deny()
	}
	data, err := readRemovalFile(receiptPath, runtimeResultLimit, true)
	var receipt runtimeResultReceipt
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err != nil || !json.Valid(data) || decoder.Decode(&receipt) != nil || receipt.SchemaVersion != 1 || !receipt.Acknowledged || receipt.Binding != binding || receipt.TaskID != taskID || receipt.MarkerHash != markerDigest(marker) || !validRuntimeResult(receipt.Envelope) || receipt.RuntimeWitness == nil || !validRuntimeWitness(*receipt.RuntimeWitness) {
		return deny()
	}
	witness, err := CaptureRuntimeRecoveryWitness(ctx, activePath, adapter)
	witness.PreviousGeneration = receipt.RuntimeWitness.PreviousGeneration
	if err != nil || witness != *receipt.RuntimeWitness {
		return deny()
	}
	// Slow observations must not allow changed durable state to become proof.
	currentMarker, markerErr := readRemovalFile(markerPath, 16384, true)
	currentReceipt, receiptErr := readRemovalFile(receiptPath, runtimeResultLimit, true)
	if markerErr != nil || receiptErr != nil || !bytes.Equal(marker, currentMarker) || !bytes.Equal(data, currentReceipt) || !runtimeMarkerAbsent(filepath.Join(dir, "inflight.json")) {
		return deny()
	}
	return RuntimeRecoveryObservation{SchemaVersion: 1, TaskID: taskID, AgentID: binding.AgentID, ServerID: binding.ServerID, MarkerHash: markerDigest(marker), ReceiptHash: markerDigest(data), EnvelopeHash: markerDigest(receipt.Envelope), Witness: witness}, nil
}
