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
	leaseNodeUIDAnnotation       = "edge.dacs.io/node-actuation-node-uid"
	leaseTransitionUIDAnnotation = "edge.dacs.io/node-actuation-handoff-uid"
	leaseIdentityHashLength      = 32
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

type LeaseRecord struct {
	Name           string
	UID            string
	HolderIdentity string
	State          nodeactuator.LockState
	RetryAfter     time.Duration
}

// NodeLock serializes CHILL Node-wide mutation by exact Node UID, holding a
// Kubernetes Lease for it. It never infers ownership from a Lease name alone
// and never deletes another holder's Lease.
type NodeLock struct {
	client  client.Client
	options LeaseOptions
}

func NewNodeLock(kubeClient client.Client, options LeaseOptions) (*NodeLock, error) {
	if kubeClient == nil {
		return nil, errors.New("Kubernetes client is required")
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}
	return &NodeLock{client: kubeClient, options: options}, nil
}

// Observe classifies the current lock without acquiring, renewing, or
// releasing it. The returned retry bound is relative to the supplied time.
func (m *NodeLock) Observe(
	ctx context.Context,
	request nodeactuator.Request,
	now time.Time,
) (LeaseRecord, error) {
	if m == nil || m.client == nil {
		return LeaseRecord{}, errors.New("Node lock is incomplete")
	}
	if err := request.ValidateForNode(request.Node); err != nil {
		return LeaseRecord{}, fmt.Errorf("validate lock observation request: %w", err)
	}
	if now.IsZero() {
		return LeaseRecord{}, errors.New("Lease observation time is required")
	}
	name := nodeLeaseName(request.Node.UID)
	holder := nodeLeaseHolder(request.Transition.UID)
	key := types.NamespacedName{Namespace: m.options.Namespace, Name: name}
	lease := &coordinationv1.Lease{}
	if err := m.client.Get(ctx, key, lease); apierrors.IsNotFound(err) {
		return LeaseRecord{Name: name, HolderIdentity: holder, State: nodeactuator.LockNotHeld}, nil
	} else if err != nil {
		return LeaseRecord{}, fmt.Errorf("get Node actuation Lease %s: %w", key, err)
	}
	if err := validateLeaseNodeIdentity(lease, request.Node.UID); err != nil {
		return LeaseRecord{}, err
	}
	expiresAt, err := leaseExpiry(lease)
	if err != nil {
		return LeaseRecord{}, err
	}
	currentHolder := ptr.Deref(lease.Spec.HolderIdentity, "")
	record := LeaseRecord{
		Name: lease.Name, UID: string(lease.UID), HolderIdentity: currentHolder,
	}
	if currentHolder != holder {
		if now.Before(expiresAt) {
			record.State = nodeactuator.LockConflict
			record.RetryAfter = expiresAt.Sub(now)
			return record, nil
		}
		record.State = nodeactuator.LockNotHeld
		return record, nil
	}
	if lease.Annotations[leaseTransitionUIDAnnotation] != request.Transition.UID {
		return LeaseRecord{}, errors.New("Node actuation Lease holder identity does not match its transition annotation")
	}
	if !now.Before(expiresAt) {
		record.State = nodeactuator.LockLost
		return record, nil
	}
	record.State = nodeactuator.LockHeld
	renewAt := expiresAt.Add(-time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second * 2 / 3)
	if now.Before(renewAt) {
		record.RetryAfter = renewAt.Sub(now)
	}
	return record, nil
}

func (m *NodeLock) AcquireOrRenew(
	ctx context.Context,
	request nodeactuator.Request,
	now time.Time,
) (LeaseRecord, error) {
	if m == nil || m.client == nil {
		return LeaseRecord{}, errors.New("Node lock is incomplete")
	}
	if err := request.ValidateForNode(request.Node); err != nil {
		return LeaseRecord{}, fmt.Errorf("validate Lease request: %w", err)
	}
	if now.IsZero() {
		return LeaseRecord{}, errors.New("Lease observation time is required")
	}
	name := nodeLeaseName(request.Node.UID)
	holder := nodeLeaseHolder(request.Transition.UID)
	key := types.NamespacedName{Namespace: m.options.Namespace, Name: name}
	lease := &coordinationv1.Lease{}
	if err := m.client.Get(ctx, key, lease); apierrors.IsNotFound(err) {
		return m.create(ctx, request, name, holder, now)
	} else if err != nil {
		return LeaseRecord{}, fmt.Errorf("get Node actuation Lease %s: %w", key, err)
	}
	if err := validateLeaseNodeIdentity(lease, request.Node.UID); err != nil {
		return LeaseRecord{}, err
	}
	expiresAt, err := leaseExpiry(lease)
	if err != nil {
		return LeaseRecord{}, err
	}
	currentHolder := ptr.Deref(lease.Spec.HolderIdentity, "")
	if currentHolder != holder && now.Before(expiresAt) {
		return LeaseRecord{
			Name: lease.Name, UID: string(lease.UID), HolderIdentity: currentHolder,
			State: nodeactuator.LockConflict, RetryAfter: expiresAt.Sub(now),
		}, nil
	}
	if currentHolder == holder && lease.Annotations[leaseTransitionUIDAnnotation] != request.Transition.UID {
		return LeaseRecord{}, errors.New("Node actuation Lease holder identity does not match its transition annotation")
	}
	if currentHolder == holder && lease.Spec.LeaseDurationSeconds != nil &&
		*lease.Spec.LeaseDurationSeconds != m.options.DurationSeconds {
		return LeaseRecord{}, errors.New("held Node actuation Lease duration differs from configured duration")
	}

	base := lease.DeepCopy()
	microNow := metav1.NewMicroTime(now.UTC())
	lease.Spec.HolderIdentity = ptr.To(holder)
	lease.Spec.LeaseDurationSeconds = ptr.To(m.options.DurationSeconds)
	lease.Spec.RenewTime = &microNow
	lease.Annotations[leaseTransitionUIDAnnotation] = request.Transition.UID
	if currentHolder != holder || !now.Before(expiresAt) {
		lease.Spec.AcquireTime = &microNow
		transitions := ptr.Deref(lease.Spec.LeaseTransitions, 0)
		lease.Spec.LeaseTransitions = ptr.To(transitions + 1)
	}
	if err := m.client.Patch(ctx, lease, client.MergeFrom(base)); err != nil {
		return LeaseRecord{}, fmt.Errorf("acquire or renew Node actuation Lease %s: %w", key, err)
	}
	return heldLeaseBinding(lease, holder, m.options.DurationSeconds), nil
}

func (m *NodeLock) Release(
	ctx context.Context,
	request nodeactuator.Request,
) (bool, error) {
	if m == nil || m.client == nil {
		return false, errors.New("Node lock is incomplete")
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
	holder := nodeLeaseHolder(request.Transition.UID)
	if ptr.Deref(lease.Spec.HolderIdentity, "") != holder ||
		lease.Annotations[leaseTransitionUIDAnnotation] != request.Transition.UID {
		return false, errors.New("refusing to release a Node actuation Lease held by another transition")
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

func (m *NodeLock) create(
	ctx context.Context,
	request nodeactuator.Request,
	name, holder string,
	now time.Time,
) (LeaseRecord, error) {
	microNow := metav1.NewMicroTime(now.UTC())
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: m.options.Namespace,
			Name:      name,
			Annotations: map[string]string{
				leaseNodeUIDAnnotation:       request.Node.UID,
				leaseTransitionUIDAnnotation: request.Transition.UID,
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
		return LeaseRecord{}, fmt.Errorf("create Node actuation Lease: %w", err)
	}
	if lease.UID == "" {
		return LeaseRecord{}, errors.New("created Node actuation Lease has incomplete API identity")
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
) LeaseRecord {
	return LeaseRecord{
		Name: lease.Name, UID: string(lease.UID), HolderIdentity: holder,
		State:      nodeactuator.LockHeld,
		RetryAfter: time.Duration(durationSeconds) * time.Second / 3,
	}
}

func nodeLeaseName(nodeUID string) string {
	return "chill-node-actuation-" + hashedLeaseIdentity(nodeUID)
}

func nodeLeaseHolder(transitionUID string) string {
	return "nodeactuation-" + hashedLeaseIdentity(transitionUID)
}

func hashedLeaseIdentity(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:leaseIdentityHashLength]
}
