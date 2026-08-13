package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/lab-paper-code/chill/internal/nodeactuator"
)

const hostBootIdentityPath = "/proc/sys/kernel/random/boot_id"

func readBootIdentity() (string, error) {
	payload, err := os.ReadFile(hostBootIdentityPath)
	if err != nil {
		return "", fmt.Errorf("read host boot identity from %s: %w", hostBootIdentityPath, err)
	}
	bootID := strings.TrimSpace(string(payload))
	if bootID == "" {
		return "", errors.New("host boot identity is empty")
	}
	return bootID, nil
}

func (r *HandoffReconciler) observeHost(
	ctx context.Context,
	request nodeactuator.Request,
) (nodeactuator.Observation, error) {
	observation, err := r.actuator.Observe(ctx, request)
	if err != nil {
		return nodeactuator.Observation{}, err
	}
	bootID, err := r.readBootIdentity()
	if err != nil {
		return nodeactuator.Observation{}, err
	}
	observation.BootID = bootID
	return observation, nil
}
