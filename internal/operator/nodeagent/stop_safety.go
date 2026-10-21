package nodeagent

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
)

// SafeToStop reports whether no Node waits on the actuation agent: a Node
// whose power-mode state is pending would keep it pending forever.
func SafeToStop(ctx context.Context, reader client.Reader) (bool, error) {
	nodes := &corev1.NodeList{}
	if err := reader.List(ctx, nodes, client.MatchingLabels{chillmeta.PowerModeState: chillmeta.PowerModePending}); err != nil {
		return false, fmt.Errorf("list pending power-mode Nodes before stopping operand: %w", err)
	}
	return len(nodes.Items) == 0, nil
}
