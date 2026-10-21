package kube

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	runnerKind = "measurement-runner"
	// runnerNodeLabel marks the runner Service of a measured Node; hand-back deletes it.
	runnerNodeLabel = "chill.dacs.io/measurement-runner"

	refusedRunnerProfileMissing       = "RunnerProfileMissing"
	refusedRunnerContainerUnsupported = "RunnerContainerUnsupported"
	refusedRunnerNamespace            = "RunnerNamespace"
	refusedRunnerPrepareFailed        = "RunnerPrepareFailed"
)

// runnerPreparation selects the runner of a transition-cost measurement: the
// newest prepared profile measured on the Node with its ModelSpec, execution
// path and first mode. A reason refuses the measurement.
func (c *SchedulerController) runnerPreparation(transition *edge.PowerModeTransitionCost, node *core.Node, config SchedulerControllerConfig) (PrepareSpec, string) {
	if transition.Spec.ServingContainer != firstfloorContainerName {
		return PrepareSpec{}, refusedRunnerContainerUnsupported
	}
	if transition.Namespace != c.observer.config.Namespace {
		return PrepareSpec{}, refusedRunnerNamespace
	}
	var best *serving.Profile
	for _, p := range c.observer.Profiles() {
		if _, prepared := config.Preparations[p.ID]; !prepared || p.Condition.NodeUID != string(node.UID) ||
			p.Condition.LogicalModel != transition.Spec.ModelSpec || p.Condition.Artifact.ExecutionPath != transition.Spec.ExecutionPath ||
			len(transition.Spec.Modes) == 0 || p.Condition.PowerMode != transition.Spec.Modes[0].PowerMode {
			continue
		}
		if best == nil || p.MeasuredAt.After(best.MeasuredAt) {
			best = &p
		}
	}
	if best == nil {
		return PrepareSpec{}, refusedRunnerProfileMissing
	}
	spec := config.Preparations[best.ID]
	sum := sha256.Sum256([]byte(transition.UID))
	spec.Name = "runner-" + hex.EncodeToString(sum[:10])
	spec.Instance.ID = spec.Name
	return spec, ""
}

// startRunner prepares the runner on the tainted Node and creates the Service
// the measurement observes, then grants the measurement. A failure refuses it
// and hands the Node back in the same run.
func (c *SchedulerController) startRunner(node *core.Node, transition *edge.PowerModeTransitionCost, spec PrepareSpec, config SchedulerControllerConfig) {
	ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	operation := Operation{ID: runnerKind + ":" + node.Name + ":" + string(transition.UID), Kind: runnerKind, Node: ref, Prepare: &spec}
	c.startNodeRun(operation, config, func(ctx context.Context, result *OperationResult) error {
		err := c.prepareRunner(ctx, transition, ref, spec)
		if err == nil {
			result.Progress = append(result.Progress, "runner ready")
			return c.annotate(ctx, transition, profilingrun.RunnerProfileAnnotation, spec.Instance.Profile.ID,
				profilingrun.AdmissionAnnotation, profilingrun.GrantedAdmission(time.Now()))
		}
		refusal := c.annotate(ctx, transition, profilingrun.AdmissionAnnotation, profilingrun.RefusedAdmission(refusedRunnerPrepareFailed, err.Error()))
		return errors.Join(err, refusal, c.handBack(ctx, ref, string(transition.UID), nil, false, result))
	})
}

func (c *SchedulerController) prepareRunner(ctx context.Context, transition *edge.PowerModeTransitionCost, node serving.NodeReference, spec PrepareSpec) error {
	if _, err := c.measurement.Execute(ctx, Operation{ID: spec.Name, Kind: operationPrepare, Node: node, Prepare: &spec}); err != nil {
		return err
	}
	pod, err := c.kube.CoreV1().Pods(c.observer.config.Namespace).Get(ctx, spec.Name, meta.GetOptions{})
	if err != nil {
		return err
	}
	service := &core.Service{
		ObjectMeta: meta.ObjectMeta{Namespace: transition.Namespace, Name: transition.Spec.ServingService, Labels: map[string]string{runnerNodeLabel: node.UID}},
		Spec: core.ServiceSpec{
			Type:     core.ServiceTypeClusterIP,
			Selector: map[string]string{instancePodUIDLabel: string(pod.UID)},
			Ports:    []core.ServicePort{{Name: "http", Protocol: core.ProtocolTCP, Port: 8080, TargetPort: intstr.FromString("http")}},
		},
	}
	_, err = c.kube.CoreV1().Services(transition.Namespace).Create(ctx, service, meta.CreateOptions{})
	return err
}
