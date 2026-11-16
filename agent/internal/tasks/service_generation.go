package tasks

import (
	"context"
	"strconv"
	"strings"
)

// ObserveGeneration is deliberately stricter than is-active: a scoped removal
// requires one running service invocation with control-group stop semantics.
func (s ServiceController) ObserveGeneration(ctx context.Context) (RuntimeGeneration, error) {
	if s.service == "" {
		return RuntimeGeneration{}, ErrCredentialRemoval
	}
	if s.run == nil {
		s.run = runCommand
	}
	if s.timeout <= 0 {
		s.timeout = defaultServiceOperationTimeout
	}
	checkCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	output, err := s.run(checkCtx, "systemctl", "show", "--property=InvocationID", "--property=MainPID", "--property=ExecMainStartTimestampMonotonic", "--property=ActiveState", "--property=SubState", "--property=KillMode", "--", s.service)
	if err != nil {
		return RuntimeGeneration{}, ErrCredentialRemoval
	}
	properties := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return RuntimeGeneration{}, ErrCredentialRemoval
		}
		if _, duplicate := properties[key]; duplicate {
			return RuntimeGeneration{}, ErrCredentialRemoval
		}
		properties[key] = value
	}
	if properties["ActiveState"] != "active" || properties["SubState"] != "running" || properties["KillMode"] != "control-group" {
		return RuntimeGeneration{}, ErrCredentialRemoval
	}
	pid, err := strconv.Atoi(properties["MainPID"])
	if err != nil {
		return RuntimeGeneration{}, ErrCredentialRemoval
	}
	generation := RuntimeGeneration{InvocationID: properties["InvocationID"], PID: pid, StartedMonotonic: properties["ExecMainStartTimestampMonotonic"]}
	if !validRemovalGeneration(generation) {
		return RuntimeGeneration{}, ErrCredentialRemoval
	}
	return generation, nil
}
