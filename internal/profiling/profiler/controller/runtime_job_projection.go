package controller

import (
	"errors"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

type runtimeJobProjection struct {
	profilingrun.RuntimeDeliveryProjection
	SecurityContext *corev1.SecurityContext
}

func projectRuntimeJob(
	config profilingrun.RunConfig,
) (runtimeJobProjection, error) {
	if !profilingrun.IsSupportedFirstfloorRunConfig(config) {
		return runtimeJobProjection{}, errors.New("no Job projection exists for the resolved RunConfig")
	}
	if config.State.Backend == profilingrun.FirstfloorORTCPUBackend {
		return runtimeJobProjection{
			SecurityContext: restrictedContainerSecurityContext(),
		}, nil
	}
	delivery, err := profilingrun.ProjectRuntimeDelivery(config.State.Backend, config.Delivery)
	if err != nil {
		return runtimeJobProjection{}, err
	}
	return runtimeJobProjection{
		RuntimeDeliveryProjection: delivery,
		SecurityContext:           runtimeAccessSecurityContext(delivery.Privileged),
	}, nil
}

func runtimeAccessSecurityContext(privileged bool) *corev1.SecurityContext {
	security := restrictedContainerSecurityContext()
	security.Privileged = ptr.To(privileged)
	security.AllowPrivilegeEscalation = ptr.To(privileged)
	return security
}
