package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/nodeactuator"
	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	leaseNodeUIDAnnotation    = "edge.dacs.io/node-actuation-node-uid"
	leaseHandoffUIDAnnotation = "edge.dacs.io/node-actuation-handoff-uid"
	leaseIdentityHashLength   = 32
)

type LeaseOptions struct {
	Namespace       string
	DurationSeconds int32
}

func (o LeaseOptions) Validate() error {
	if strings.TrimSpace(o.Namespace) == "" || strings.TrimSpace(o.Namespace) != o.Namespace {
		return errors.New("NodeActuator Lease namespace is required without surrounding whitespace")
	}
	if o.DurationSeconds < 5 || o.DurationSeconds > 300 {
		return errors.New("NodeActuator Lease duration must be between 5 seconds and 5 minutes")
	}
	return nil
}

type LeaseBinding struct {
	Name           string
	UID            string
	HolderIdentity string
	State          nodeactuator.LeaseState
	RetryAfter     time.Duration
}

// NodeLeaseManager serializes CHILL Node-wide mutation by exact Node UID. It
// never infers ownership from a Lease name alone and never deletes another
// holder's Lease.
type NodeLeaseManager struct {
	client  client.Client
	options LeaseOptions
}

func NewNodeLeaseManager(kubeClient client.Client, options LeaseOptions) (*NodeLeaseManager, error) {
	if kubeClient == nil {
		return nil, errors.New("Kubernetes client is required")
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}
	return &NodeLeaseManager{client: kubeClient, options: options}, nil
}

// Observe classifies the current lock without acquiring, renewing, or
// releasing it. The returned retry bound is relative to the supplied time.
func (m *NodeLeaseManager) Observe(
	ctx context.Context,
	request nodeactuator.Request,
	now time.Time,
) (LeaseBinding, error) {
	if m == nil || m.client == nil {
		return LeaseBinding{}, errors.New("Node Lease manager is incomplete")
	}
	if err := request.ValidateForNode(request.Node); err != nil {
		return LeaseBinding{}, fmt.Errorf("validate Lease observation request: %w", err)
	}
	if now.IsZero() {
		return LeaseBinding{}, errors.New("Lease observation time is required")
	}
	name := nodeLeaseName(request.Node.UID)
	holder := nodeLeaseHolder(request.Handoff.UID)
	key := types.NamespacedName{Namespace: m.options.Namespace, Name: name}
	lease := &coordinationv1.Lease{}
	if err := m.client.Get(ctx, key, lease); apierrors.IsNotFound(err) {
		return LeaseBinding{Name: name, HolderIdentity: holder, State: nodeactuator.LeaseNotHeld}, nil
	} else if err != nil {
		return LeaseBinding{}, fmt.Errorf("get Node actuation Lease %s: %w", key, err)
	}
	if err := validateLeaseNodeIdentity(lease, request.Node.UID); err != nil {
		return LeaseBinding{}, err
	}
	expiresAt, err := leaseExpiry(lease)
	if err != nil {
		return LeaseBinding{}, err
	}
	currentHolder := ptr.Deref(lease.Spec.HolderIdentity, "")
	binding := LeaseBinding{
		Name: lease.Name, UID: string(lease.UID), HolderIdentity: currentHolder,
	}
	if currentHolder != holder {
		if now.Before(expiresAt) {
			binding.State = nodeactuator.LeaseConflict
			binding.RetryAfter = expiresAt.Sub(now)
			return binding, nil
		}
		binding.State = nodeactuator.LeaseNotHeld
		return binding, nil
	}
	if lease.Annotations[leaseHandoffUIDAnnotation] != request.Handoff.UID {
		return LeaseBinding{}, errors.New("Node actuation Lease holder identity does not match its handoff annotation")
	}
	if !now.Before(expiresAt) {
		binding.State = nodeactuator.LeaseLost
		return binding, nil
	}
	binding.State = nodeactuator.LeaseHeld
	renewAt := expiresAt.Add(-time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second * 2 / 3)
	if now.Before(renewAt) {
		binding.RetryAfter = renewAt.Sub(now)
	}
	return binding, nil
}

func (m *NodeLeaseManager) AcquireOrRenew(
	ctx context.Context,
	request nodeactuator.Request,
	now time.Time,
) (LeaseBinding, error) {
	if m == nil || m.client == nil {
		return LeaseBinding{}, errors.New("Node Lease manager is incomplete")
	}
	if err := request.ValidateForNode(request.Node); err != nil {
		return LeaseBinding{}, fmt.Errorf("validate Lease request: %w", err)
	}
	if now.IsZero() {
		return LeaseBinding{}, errors.New("Lease observation time is required")
	}
	name := nodeLeaseName(request.Node.UID)
	holder := nodeLeaseHolder(request.Handoff.UID)
	key := types.NamespacedName{Namespace: m.options.Namespace, Name: name}
	lease := &coordinationv1.Lease{}
	if err := m.client.Get(ctx, key, lease); apierrors.IsNotFound(err) {
		return m.create(ctx, request, name, holder, now)
	} else if err != nil {
		return LeaseBinding{}, fmt.Errorf("get Node actuation Lease %s: %w", key, err)
	}
	if err := validateLeaseNodeIdentity(lease, request.Node.UID); err != nil {
		return LeaseBinding{}, err
	}
	expiresAt, err := leaseExpiry(lease)
	if err != nil {
		return LeaseBinding{}, err
	}
	currentHolder := ptr.Deref(lease.Spec.HolderIdentity, "")
	if currentHolder != holder && now.Before(expiresAt) {
		return LeaseBinding{
			Name: lease.Name, UID: string(lease.UID), HolderIdentity: currentHolder,
			State: nodeactuator.LeaseConflict, RetryAfter: expiresAt.Sub(now),
		}, nil
	}
	if currentHolder == holder && lease.Annotations[leaseHandoffUIDAnnotation] != request.Handoff.UID {
		return LeaseBinding{}, errors.New("Node actuation Lease holder identity does not match its handoff annotation")
	}
	if currentHolder == holder && lease.Spec.LeaseDurationSeconds != nil &&
		*lease.Spec.LeaseDurationSeconds != m.options.DurationSeconds {
		return LeaseBinding{}, errors.New("held Node actuation Lease duration differs from configured duration")
	}

	base := lease.DeepCopy()
	microNow := metav1.NewMicroTime(now.UTC())
	lease.Spec.HolderIdentity = ptr.To(holder)
	lease.Spec.LeaseDurationSeconds = ptr.To(m.options.DurationSeconds)
	lease.Spec.RenewTime = &microNow
	lease.Annotations[leaseHandoffUIDAnnotation] = request.Handoff.UID
	if currentHolder != holder || !now.Before(expiresAt) {
		lease.Spec.AcquireTime = &microNow
		transitions := ptr.Deref(lease.Spec.LeaseTransitions, 0)
		lease.Spec.LeaseTransitions = ptr.To(transitions + 1)
	}
	if err := m.client.Patch(ctx, lease, client.MergeFrom(base)); err != nil {
		return LeaseBinding{}, fmt.Errorf("acquire or renew Node actuation Lease %s: %w", key, err)
	}
	return heldLeaseBinding(lease, holder, m.options.DurationSeconds), nil
}

func (m *NodeLeaseManager) Release(
	ctx context.Context,
	request nodeactuator.Request,
) (bool, error) {
	if m == nil || m.client == nil {
		return false, errors.New("Node Lease manager is incomplete")
	}
	if err := request.ValidateForNode(request.Node); err != nil {
		return false, fmt.Errorf("validate Lease release request: %w", err)
	}
	key := types.NamespacedName{
		Namespace: m.options.Namespace,
		Name:      nodeLeaseName(request.Node.UID),
	}
	lease := &coordinationv1.Lease{}
	if err := m.client.Get(ctx, key, lease); apierrors.IsNotFound(err) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("get Node actuation Lease %s for release: %w", key, err)
	}
	if err := validateLeaseNodeIdentity(lease, request.Node.UID); err != nil {
		return false, err
	}
	holder := nodeLeaseHolder(request.Handoff.UID)
	if ptr.Deref(lease.Spec.HolderIdentity, "") != holder ||
		lease.Annotations[leaseHandoffUIDAnnotation] != request.Handoff.UID {
		return false, errors.New("refusing to release a Node actuation Lease held by another handoff")
	}
	uid := lease.UID
	resourceVersion := lease.ResourceVersion
	if err := m.client.Delete(ctx, lease, &client.DeleteOptions{Preconditions: &metav1.Preconditions{
		UID: &uid, ResourceVersion: &resourceVersion,
	}}); err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("release Node actuation Lease %s: %w", key, err)
	}
	return true, nil
}

func (m *NodeLeaseManager) create(
	ctx context.Context,
	request nodeactuator.Request,
	name, holder string,
	now time.Time,
) (LeaseBinding, error) {
	microNow := metav1.NewMicroTime(now.UTC())
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: m.options.Namespace,
			Name:      name,
			Annotations: map[string]string{
				leaseNodeUIDAnnotation:    request.Node.UID,
				leaseHandoffUIDAnnotation: request.Handoff.UID,
			},
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       ptr.To(holder),
			LeaseDurationSeconds: ptr.To(m.options.DurationSeconds),
			AcquireTime:          &microNow,
			RenewTime:            &microNow,
			LeaseTransitions:     ptr.To(int32(0)),
		},
	}
	if err := m.client.Create(ctx, lease); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return m.AcquireOrRenew(ctx, request, now)
		}
		return LeaseBinding{}, fmt.Errorf("create Node actuation Lease: %w", err)
	}
	if lease.UID == "" {
		return LeaseBinding{}, errors.New("created Node actuation Lease has incomplete API identity")
	}
	return heldLeaseBinding(lease, holder, m.options.DurationSeconds), nil
}

func validateLeaseNodeIdentity(lease *coordinationv1.Lease, nodeUID string) error {
	if lease == nil || lease.Namespace == "" || lease.Name != nodeLeaseName(nodeUID) || lease.UID == "" ||
		lease.Annotations[leaseNodeUIDAnnotation] != nodeUID {
		return errors.New("Node actuation Lease has invalid API or Node identity")
	}
	if lease.Spec.HolderIdentity == nil || strings.TrimSpace(*lease.Spec.HolderIdentity) == "" ||
		lease.Spec.LeaseDurationSeconds == nil || *lease.Spec.LeaseDurationSeconds < 1 {
		return errors.New("Node actuation Lease has incomplete holder or duration")
	}
	return nil
}

func leaseExpiry(lease *coordinationv1.Lease) (time.Time, error) {
	var base time.Time
	if lease.Spec.RenewTime != nil {
		base = lease.Spec.RenewTime.Time
	} else if lease.Spec.AcquireTime != nil {
		base = lease.Spec.AcquireTime.Time
	}
	if base.IsZero() || lease.Spec.LeaseDurationSeconds == nil || *lease.Spec.LeaseDurationSeconds < 1 {
		return time.Time{}, errors.New("Node actuation Lease has no trustworthy expiry")
	}
	return base.Add(time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second), nil
}

func heldLeaseBinding(
	lease *coordinationv1.Lease,
	holder string,
	durationSeconds int32,
) LeaseBinding {
	return LeaseBinding{
		Name: lease.Name, UID: string(lease.UID), HolderIdentity: holder,
		State:      nodeactuator.LeaseHeld,
		RetryAfter: time.Duration(durationSeconds) * time.Second / 3,
	}
}

func nodeLeaseName(nodeUID string) string {
	return "chill-node-actuation-" + hashedLeaseIdentity(nodeUID)
}

func nodeLeaseHolder(handoffUID string) string {
	return "nodeactuation-" + hashedLeaseIdentity(handoffUID)
}

func hashedLeaseIdentity(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:leaseIdentityHashLength]
}
