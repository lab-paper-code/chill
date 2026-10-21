package controller

import (
	"context"
	"encoding/json"
	"errors"
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

// unpublished marks that this process has not yet written the annotation, so
// the first read, or the first failed read, always patches.
const unpublished = "\x00"

// Start runs until ctx ends. A failed read removes the annotation: a host
// that cannot be read has no current read-back.
func (p ReadBackPublisher) Start(ctx context.Context) error {
	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()
	last := unpublished
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
		if *last != "" {
			if clearErr := p.patch(ctx, nil); clearErr != nil {
				return errors.Join(err, clearErr)
			}
			*last = ""
		}
		return err
	}
	raw, err := json.Marshal(projectAPITarget(observation.Target))
	if err != nil {
		return err
	}
	if *last == string(raw) {
		return nil
	}
	value := string(raw)
	if err = p.patch(ctx, &value); err != nil {
		return err
	}
	*last = value
	return nil
}

// patch sets the read-back annotation, or removes it when value is nil.
func (p ReadBackPublisher) patch(ctx context.Context, value *string) error {
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{
		"annotations": map[string]*string{metadata.PowerActuator: value},
	}})
	if err != nil {
		return err
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: p.Node}}
	return p.Client.Patch(ctx, node, client.RawPatch(types.MergePatchType, patch))
}
