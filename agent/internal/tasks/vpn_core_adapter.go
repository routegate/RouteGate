package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ikaevus/routegate/agent/internal/platform"
)

// VPNCoreAdapter owns the VPN Core-specific parts of the Agent apply
// lifecycle. Atomic file promotion and rollback remain shared Agent behavior.
type VPNCoreAdapter interface {
	Descriptor() platform.VPNCoreAdapterDescriptor
	Stage(ConfigTask) (StageResult, error)
	Validate(context.Context, string) (ValidationResult, error)
	Restart(context.Context) (ServiceResult, error)
	Stop(context.Context) (ServiceResult, error)
	IsActive(context.Context) (ServiceResult, error)
	IsEnabled(context.Context) (ServiceResult, error)
	ExecuteServiceTask(context.Context, ConfigTask) (ServiceTaskReport, error)
	CheckHealth(context.Context, string) (ListenerHealthResult, error)
}

type renderedVPNCoreDescriptor struct {
	Core      string `json:"core"`
	Protocol  string `json:"protocol"`
	Transport string `json:"transport"`
	Security  string `json:"security"`
}

func SelectVPNCoreAdapters(task ConfigTask, vless, wireGuard VPNCoreAdapter, additional ...VPNCoreAdapter) ([]VPNCoreAdapter, error) {
	var envelope struct {
		Metadata struct {
			VPNCore  renderedVPNCoreDescriptor   `json:"vpnCore"`
			VPNCores []renderedVPNCoreDescriptor `json:"vpnCores"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(task.RenderedConfig, &envelope); err != nil {
		return nil, errors.New("rendered config must be valid JSON")
	}

	descriptors := envelope.Metadata.VPNCores
	if len(descriptors) == 0 {
		descriptors = []renderedVPNCoreDescriptor{envelope.Metadata.VPNCore}
	}
	candidates := append([]VPNCoreAdapter{vless, wireGuard}, additional...)
	selected := make([]VPNCoreAdapter, 0, len(descriptors))
	seenCores := map[string]struct{}{}

	for _, descriptor := range descriptors {
		if strings.TrimSpace(descriptor.Core) == "" {
			if vless == nil {
				return nil, errors.New("VLESS VPN Core adapter is unavailable")
			}
			if _, exists := seenCores[vless.Descriptor().Core]; !exists {
				selected = append(selected, vless)
				seenCores[vless.Descriptor().Core] = struct{}{}
			}
			continue
		}

		var matched VPNCoreAdapter
		for _, candidate := range candidates {
			if candidate == nil {
				continue
			}
			managed := candidate.Descriptor()
			if managed.Core == descriptor.Core && managed.Protocol == descriptor.Protocol &&
				containsString(managed.Transports, descriptor.Transport) && containsString(managed.SecurityModes, descriptor.Security) {
				matched = candidate
				break
			}
		}
		if matched == nil {
			return nil, errors.New("rendered config selects an unsupported VPN Core adapter")
		}

		// VLESS and Shadowsocks share one sing-box config and one service. The
		// rendered singBox object already contains both inbounds, so applying the
		// same service twice would add no safety and would complicate rollback.
		core := matched.Descriptor().Core
		if _, exists := seenCores[core]; exists {
			continue
		}
		seenCores[core] = struct{}{}
		selected = append(selected, matched)
	}
	if len(selected) == 0 {
		return nil, errors.New("rendered config contains no managed VPN Core adapters")
	}
	return selected, nil
}

func SelectVPNCoreAdapter(task ConfigTask, vless, wireGuard VPNCoreAdapter, additional ...VPNCoreAdapter) (VPNCoreAdapter, error) {
	adapters, err := SelectVPNCoreAdapters(task, vless, wireGuard, additional...)
	if err != nil {
		return nil, err
	}
	return adapters[0], nil
}

func containsString(values []string, selected string) bool {
	for _, value := range values {
		if value == selected {
			return true
		}
	}
	return false
}

type singBoxVLESSAdapter struct {
	stager      Stager
	validator   Validator
	service     ServiceController
	descriptor  platform.VPNCoreAdapterDescriptor
	inboundType string
	// realityCheck probes Reality handshake targets from this node before the
	// staged config replaces the running one. Nil skips the probe.
	realityCheck func(context.Context, string) error
}

var _ VPNCoreAdapter = singBoxVLESSAdapter{}

func NewSingBoxVLESSAdapter(stagingDir, binary, service string) VPNCoreAdapter {
	return singBoxVLESSAdapter{
		stager:       NewStager(stagingDir),
		validator:    NewValidator(binary),
		service:      NewServiceController(service),
		descriptor:   platform.ManagedVPNCoreAdapters()[0],
		inboundType:  platform.VPNProtocolVLESS,
		realityCheck: CheckRealityHandshakeTargets,
	}
}

func NewSingBoxShadowsocksAdapter(stagingDir, binary, service string) VPNCoreAdapter {
	return singBoxVLESSAdapter{
		stager: NewStager(stagingDir), validator: NewValidator(binary),
		service: NewServiceController(service), descriptor: platform.ManagedVPNCoreAdapters()[3],
		inboundType: platform.VPNProtocolShadowsocks, realityCheck: CheckRealityHandshakeTargets,
	}
}

func (a singBoxVLESSAdapter) Descriptor() platform.VPNCoreAdapterDescriptor {
	return a.descriptor
}

func (a singBoxVLESSAdapter) Stage(task ConfigTask) (StageResult, error) {
	return a.stager.Stage(task)
}

func (a singBoxVLESSAdapter) Validate(ctx context.Context, configPath string) (ValidationResult, error) {
	result, err := a.validator.Check(ctx, configPath)
	if err != nil || a.realityCheck == nil {
		return result, err
	}
	return result, a.realityCheck(ctx, configPath)
}

func (a singBoxVLESSAdapter) Restart(ctx context.Context) (ServiceResult, error) {
	return a.service.Restart(ctx)
}

func (a singBoxVLESSAdapter) Stop(ctx context.Context) (ServiceResult, error) {
	return a.service.Stop(ctx)
}

func (a singBoxVLESSAdapter) IsActive(ctx context.Context) (ServiceResult, error) {
	return a.service.IsActive(ctx)
}

func (a singBoxVLESSAdapter) IsEnabled(ctx context.Context) (ServiceResult, error) {
	return a.service.IsEnabled(ctx)
}

func (a singBoxVLESSAdapter) ObserveGeneration(ctx context.Context, activePath string) (RuntimeGeneration, error) {
	generation, err := a.service.ObserveGeneration(ctx)
	if err != nil {
		return RuntimeGeneration{}, ErrCredentialRemoval
	}
	if err := verifyRemovalProcessSource(generation.PID, a.validator.binary, activePath); err != nil {
		return RuntimeGeneration{}, ErrCredentialRemoval
	}
	stable, err := a.service.ObserveGeneration(ctx)
	if err != nil || stable != generation {
		return RuntimeGeneration{}, ErrCredentialRemoval
	}
	return generation, nil
}

func (a singBoxVLESSAdapter) ExecuteServiceTask(ctx context.Context, task ConfigTask) (ServiceTaskReport, error) {
	return ExecuteServiceTask(ctx, a.service, task)
}

func (a singBoxVLESSAdapter) CheckHealth(ctx context.Context, configPath string) (ListenerHealthResult, error) {
	// Shared sing-box configs must pass every managed listener check. The
	// evidence attached to this adapter must describe its own protocol,
	// rather than the last inbound in the shared config.
	if _, err := CheckSingBoxTCPListeners(ctx, configPath); err != nil {
		return ListenerHealthResult{}, err
	}
	return CheckSingBoxTCPListener(ctx, configPath, a.inboundType)
}
