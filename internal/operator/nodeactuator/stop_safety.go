package nodeactuator

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
	handoffs := &edgev1alpha1.NodeActuationList{}
	if err := reader.List(ctx, handoffs, client.InNamespace(namespace)); err != nil {
		return false, fmt.Errorf("list NodeActuations before stopping operand: %w", err)
	}
	for i := range handoffs.Items {
		if handoffNeedsOperand(&handoffs.Items[i]) {
			return false, nil
		}
	}
	return true, nil
}

func handoffNeedsOperand(handoff *edgev1alpha1.NodeActuation) bool {
	if handoff == nil {
		return true
	}
	status := handoff.Status
	if status.Lease != nil {
		return true
	}
	if status.Phase == edgev1alpha1.NodeActuationPhaseReady ||
		status.Phase == edgev1alpha1.NodeActuationPhaseFailed {
		return false
	}
	return true
}
