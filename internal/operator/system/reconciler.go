package system

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

// ChillSystemReconciler maintains the root CHILL status resource.
type ChillSystemReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Options Options
}

// +kubebuilder:rbac:groups=edge.dacs.io,resources=chillsystems,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=edge.dacs.io,resources=chillsystems/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=edge.dacs.io,resources=deviceclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments;daemonsets,verbs=get;list;watch

// Reconcile updates the singleton ChillSystem status.
func (r *ChillSystemReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.NamespacedName != r.systemKey() {
		return ctrl.Result{}, nil
	}
	return r.reconcileSystem(ctx)
}

func (r *ChillSystemReconciler) reconcileSystem(ctx context.Context) (ctrl.Result, error) {
	key := r.systemKey()
	system := &edgev1alpha1.ChillSystem{}
	if err := r.Get(ctx, key, system); err != nil {
		if apierrors.IsNotFound(err) {
			system = &edgev1alpha1.ChillSystem{
				ObjectMeta: metav1.ObjectMeta{
					Name:      key.Name,
					Namespace: key.Namespace,
				},
			}
			if err := r.Create(ctx, system); err != nil && !apierrors.IsAlreadyExists(err) {
				return ctrl.Result{}, fmt.Errorf("create ChillSystem %s: %w", key.String(), err)
			}
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get ChillSystem %s: %w", key.String(), err)
	}

	next := buildStatus(r.observe(ctx, system), system.Status.Conditions, metav1Now())
	if statusesEqual(system.Status, next) {
		return ctrl.Result{}, nil
	}
	system.Status = next
	if err := r.Status().Update(ctx, system); err != nil {
		return ctrl.Result{}, fmt.Errorf("update ChillSystem %s status: %w", key.String(), err)
	}
	return ctrl.Result{}, nil
}

func (r *ChillSystemReconciler) systemKey() types.NamespacedName {
	return types.NamespacedName{Namespace: r.namespace(), Name: r.systemName()}
}
