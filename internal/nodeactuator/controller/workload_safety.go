package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/nodeactuator"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const maximumRecordedBlockingPods = 32

type WorkloadSafetyOptions struct {
	AllowedNamespaces []string
	AllowedPodUIDs    []string
}

func (o WorkloadSafetyOptions) Validate() error {
	if len(o.AllowedNamespaces) == 0 {
		return errors.New("at least one allowed workload namespace is required")
	}
	seen := make(map[string]struct{}, len(o.AllowedNamespaces))
	for _, namespace := range o.AllowedNamespaces {
		if strings.TrimSpace(namespace) != namespace || len(validation.IsDNS1123Label(namespace)) != 0 {
			return fmt.Errorf("invalid allowed workload namespace %q", namespace)
		}
		if _, duplicate := seen[namespace]; duplicate {
			return fmt.Errorf("duplicate allowed workload namespace %q", namespace)
		}
		seen[namespace] = struct{}{}
	}
	seenUIDs := make(map[string]struct{}, len(o.AllowedPodUIDs))
	for _, uid := range o.AllowedPodUIDs {
		if strings.TrimSpace(uid) == "" || strings.TrimSpace(uid) != uid {
			return errors.New("allowed workload Pod UID is required without surrounding whitespace")
		}
		if _, duplicate := seenUIDs[uid]; duplicate {
			return fmt.Errorf("duplicate allowed workload Pod UID %q", uid)
		}
		seenUIDs[uid] = struct{}{}
	}
	return nil
}

type BlockingPod struct {
	Namespace string
	Name      string
	UID       string
}

type WorkloadSafetyResult struct {
	Allowed      bool
	ObservedAt   time.Time
	BlockingPods []BlockingPod
}

func (r WorkloadSafetyResult) Validate() error {
	if r.ObservedAt.IsZero() {
		return errors.New("workload safety observation time is required")
	}
	if r.Allowed != (len(r.BlockingPods) == 0) {
		return errors.New("workload safety verdict contradicts blocking Pods")
	}
	if len(r.BlockingPods) > maximumRecordedBlockingPods {
		return fmt.Errorf("workload safety result exceeds the %d-object status bound", maximumRecordedBlockingPods)
	}
	for _, pod := range r.BlockingPods {
		if strings.TrimSpace(pod.Namespace) == "" || strings.TrimSpace(pod.Namespace) != pod.Namespace ||
			strings.TrimSpace(pod.Name) == "" || strings.TrimSpace(pod.Name) != pod.Name ||
			strings.TrimSpace(pod.UID) == "" || strings.TrimSpace(pod.UID) != pod.UID {
			return errors.New("blocking Pod identity is incomplete")
		}
	}
	return nil
}

// KubernetesWorkloadSafetyChecker lists the exact target Node's non-terminal
// Pods from the API boundary and rejects every namespace not explicitly
// installed as allowed. It never infers safety from a cached count.
type KubernetesWorkloadSafetyChecker struct {
	reader            client.Reader
	allowedNamespaces []string
	allowedPodUIDs    []string
	now               func() time.Time
}

func NewKubernetesWorkloadSafetyChecker(
	reader client.Reader,
	options WorkloadSafetyOptions,
) (*KubernetesWorkloadSafetyChecker, error) {
	if reader == nil {
		return nil, errors.New("Kubernetes Pod reader is required")
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}
	allowed := append([]string(nil), options.AllowedNamespaces...)
	slices.Sort(allowed)
	allowedPodUIDs := append([]string(nil), options.AllowedPodUIDs...)
	slices.Sort(allowedPodUIDs)
	return &KubernetesWorkloadSafetyChecker{
		reader: reader, allowedNamespaces: allowed, allowedPodUIDs: allowedPodUIDs, now: time.Now,
	}, nil
}

func (c *KubernetesWorkloadSafetyChecker) Check(
	ctx context.Context,
	request nodeactuator.Request,
) (WorkloadSafetyResult, error) {
	if c == nil || c.reader == nil || c.now == nil {
		return WorkloadSafetyResult{}, errors.New("workload safety checker is incomplete")
	}
	if err := request.ValidateForNode(request.Node); err != nil {
		return WorkloadSafetyResult{}, fmt.Errorf("validate workload safety request: %w", err)
	}
	pods := &corev1.PodList{}
	if err := c.reader.List(ctx, pods, client.MatchingFields{"spec.nodeName": request.Node.Name}); err != nil {
		return WorkloadSafetyResult{}, fmt.Errorf("list Pods on target Node %q: %w", request.Node.Name, err)
	}
	blocking := make([]BlockingPod, 0)
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Spec.NodeName != request.Node.Name || pod.Status.Phase == corev1.PodSucceeded ||
			pod.Status.Phase == corev1.PodFailed || slices.Contains(c.allowedNamespaces, pod.Namespace) ||
			slices.Contains(c.allowedPodUIDs, string(pod.UID)) {
			continue
		}
		if pod.Namespace == "" || pod.Name == "" || pod.UID == "" {
			return WorkloadSafetyResult{}, errors.New("blocking Pod has incomplete API identity")
		}
		blocking = append(blocking, BlockingPod{
			Namespace: pod.Namespace, Name: pod.Name, UID: string(pod.UID),
		})
	}
	if len(blocking) > maximumRecordedBlockingPods {
		return WorkloadSafetyResult{}, fmt.Errorf(
			"target Node has %d blocking Pods, exceeding the %d-object status bound",
			len(blocking), maximumRecordedBlockingPods,
		)
	}
	sort.Slice(blocking, func(i, j int) bool {
		if blocking[i].Namespace != blocking[j].Namespace {
			return blocking[i].Namespace < blocking[j].Namespace
		}
		if blocking[i].Name != blocking[j].Name {
			return blocking[i].Name < blocking[j].Name
		}
		return blocking[i].UID < blocking[j].UID
	})
	result := WorkloadSafetyResult{
		Allowed: len(blocking) == 0, ObservedAt: c.now().UTC(), BlockingPods: blocking,
	}
	if err := result.Validate(); err != nil {
		return WorkloadSafetyResult{}, err
	}
	return result, nil
}
