package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SafeToStop proves that no retained transition still depends on the operand
// for progress or Lease release. Unknown and non-terminal states block
// DaemonSet deletion.
func SafeToStop(ctx context.Context, reader client.Reader, namespace string) (bool, error) {
	if reader == nil {
		return false, errors.New("NodeActuation reader is required")
	}
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(namespace) != namespace {
		return false, errors.New("NodeActuation namespace is required without surrounding whitespace")
	}
	transitions := &edgev1alpha1.NodeActuationList{}
	if err := reader.List(ctx, transitions, client.InNamespace(namespace)); err != nil {
		return false, fmt.Errorf("list NodeActuations before stopping operand: %w", err)
	}
	for i := range transitions.Items {
		if transitionNeedsOperand(&transitions.Items[i]) {
			return false, nil
		}
	}
	return true, nil
}

func transitionNeedsOperand(transition *edgev1alpha1.NodeActuation) bool {
	if transition == nil {
		return true
	}
	status := transition.Status
	if status.Lease != nil {
		return true
	}
	if status.Phase == edgev1alpha1.NodeActuationPhaseReady ||
		status.Phase == edgev1alpha1.NodeActuationPhaseFailed {
		return false
	}
	return true
}
