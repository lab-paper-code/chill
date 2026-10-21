package controller

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReadBackPublisher writes the host's current actuator target to its Node
// annotation every interval, patching only when the value changes. ChillServer reads it
// when no NodeActuation is Ready, as after a reboot interrupts a switch.
type ReadBackPublisher struct {
	Client   client.Client
	Host     HostActuator
	Node     string
	Interval time.Duration
	Timeout  time.Duration
}

// Start runs until ctx ends; a failed read leaves the last published value.
func (p ReadBackPublisher) Start(ctx context.Context) error {
	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()
	last := ""
	for {
		if err := p.publish(ctx, &last); err != nil {
			ctrl.Log.Error(err, "publish power actuator read-back", "node", p.Node)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (p ReadBackPublisher) publish(ctx context.Context, last *string) error {
	observation, err := p.Host.Observe(ctx, nodeactuator.Request{
		Target: nodeactuator.Target{
			Kind: nodeactuator.ActuatorKindJetsonNVPModel, JetsonNVPModel: &nodeactuator.JetsonNVPModelTarget{},
		},
		Deadline: time.Now().Add(p.Timeout),
	})
	if err != nil {
		return err
	}
	raw, err := json.Marshal(projectAPITarget(observation.Target))
	if err != nil {
		return err
	}
	if *last == string(raw) {
		return nil
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{
		"annotations": map[string]string{metadata.PowerActuator: string(raw)},
	}})
	if err != nil {
		return err
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: p.Node}}
	if err = p.Client.Patch(ctx, node, client.RawPatch(types.MergePatchType, patch)); err != nil {
		return err
	}
	*last = string(raw)
	return nil
}
