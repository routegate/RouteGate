package tasks

import (
	"context"
	"os"
)

// runtimeRecoveryReadOnlyHoldV1 retains Agent's existing exclusive mutation
// flock across a fresh, strictly read-only recovery observation. It exists
// solely as an internal building block for later reviewed recovery design.
//
// It is NOT an unlock authorization, host attestation, an outbound protocol,
// or a durable recovery approval. In particular its Close method never
// removes mutation-inflight.json or mutation-result.json.
type runtimeRecoveryReadOnlyHoldV1 struct {
	lock        *os.File
	observation RuntimeRecoveryObservation
}

// beginRuntimeRecoveryReadOnlyHoldV1 deliberately has no public call site.
// It does not rely on the previous HTTP observation or a bearer-token MAC.
// Instead it re-reads the acknowledged on-disk receipt, durable marker,
// active configuration and sing-box generation under one fresh local lock.
// Any uncertainty leaves the original durable mutation fence untouched.
func beginRuntimeRecoveryReadOnlyHoldV1(
	ctx context.Context,
	dir string,
	binding RuntimeResultBinding,
	taskID, activePath string,
	adapter VPNCoreAdapter,
) (*runtimeRecoveryReadOnlyHoldV1, error) {
	if ctx == nil || ctx.Err() != nil ||
		!canonicalTaskIDPattern.MatchString(taskID) ||
		!RuntimeMutationStatePresent(dir) {
		return nil, ErrRuntimeMutationBlocked
	}
	lock, err := lockRuntimeMutation(dir)
	if err != nil {
		return nil, ErrRuntimeMutationBlocked
	}
	observed, err := observeRuntimeRecoveryUnderLock(ctx, dir, binding, taskID, activePath, adapter)
	if err != nil {
		_ = lock.Close()
		return nil, ErrRuntimeMutationBlocked
	}
	if ctx.Err() != nil {
		_ = lock.Close()
		return nil, ErrRuntimeMutationBlocked
	}
	return &runtimeRecoveryReadOnlyHoldV1{lock: lock, observation: observed}, nil
}

// Close releases ONLY the ephemeral flock, never the durable failure fence.
// The held observation is historical after Close and cannot be promoted into
// recovery authorization. No caller may use this type to mutate the runtime.
func (h *runtimeRecoveryReadOnlyHoldV1) Close() error {
	if h == nil {
		return ErrRuntimeMutationBlocked
	}
	// Once the exclusive flock is gone, this observation is historical and
	// must not remain readable via a previously retained hold pointer.
	// This clears the in-memory snapshot even if closing the lock fails.
	h.observation = RuntimeRecoveryObservation{}
	if h.lock == nil {
		return nil
	}
	err := h.lock.Close()
	h.lock = nil
	if err != nil {
		return ErrRuntimeMutationBlocked
	}
	return nil
}
