package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go"
	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type inspectionStore struct {
	err      error
	body     string
	location evidencestore.ObjectLocation
	opens    int
}

func (s *inspectionStore) OpenLocation(_ context.Context, location evidencestore.ObjectLocation) (io.ReadCloser, error) {
	s.opens++
	s.location = location
	if s.err != nil {
		return nil, s.err
	}
	return io.NopCloser(strings.NewReader(s.body)), nil
}

type inspectionReader struct {
	client.Reader
	getFailure  string
	listFailure bool
}

func (r inspectionReader) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	if key.Name == r.getFailure {
		return errors.New("sensitive API details")
	}
	return r.Reader.Get(ctx, key, object, options...)
}

func (r inspectionReader) List(ctx context.Context, objects client.ObjectList, options ...client.ListOption) error {
	applied := &client.ListOptions{}
	for _, option := range options {
		option.ApplyToList(applied)
	}
	if applied.Namespace != "measurement" || (applied.LabelSelector != nil && !applied.LabelSelector.Empty()) {
		return errors.New("discovery must list namespace without invalid label selector")
	}
	if r.listFailure {
		return errors.New("sensitive list details")
	}
	return r.Reader.List(ctx, objects, options...)
}

func TestInspectPretrialFailureBoundaries(t *testing.T) {
	grant := time.Date(2026, 11, 6, 1, 2, 3, 0, time.UTC)
	for _, test := range []struct {
		name       string
		change     func(*edgev1alpha1.BatchEnergyOptimization, *corev1.Node)
		runOwner   string
		storeErr   error
		plan       string
		getFailure string
		listError  bool
		wrongUID   bool
		cancel     bool
		eligible   bool
	}{
		{name: "invalid legacy metadata", eligible: true},
		{name: "foreign UID Run is not owned", runOwner: "foreign", eligible: true},
		{name: "foreign API version Run is not owned", runOwner: "version", eligible: true},
		{name: "foreign kind Run is not owned", runOwner: "kind", eligible: true},
		{name: "noncontroller Run is not owned", runOwner: "noncontroller", eligible: true},
		{name: "owned unlabeled Run", runOwner: "owned"},
		{name: "valid metadata", change: func(p *edgev1alpha1.BatchEnergyOptimization, _ *corev1.Node) { p.Name = "valid-campaign" }},
		{name: "replaced request UID", wrongUID: true},
		{name: "request read error", getFailure: "parent"},
		{name: "Node read error", getFailure: "node"},
		{name: "Run list error", listError: true},
		{name: "store error", storeErr: errors.New("sensitive endpoint credentials")},
		{name: "lost bucket", storeErr: fmt.Errorf("%w: %w", evidencestore.ErrNotFound, &smithy.GenericAPIError{Code: "NoSuchBucket"})},
		{name: "wrapped missing key", storeErr: fmt.Errorf("read: %w: %w", evidencestore.ErrNotFound, &smithy.GenericAPIError{Code: "NoSuchKey"}), eligible: true},
		{name: "plan present", plan: `{"schemaVersion":"existing"}`},
		{name: "malformed plan present", plan: "malformed"},
		{name: "published result", change: func(p *edgev1alpha1.BatchEnergyOptimization, _ *corev1.Node) { p.Status.Result = &edgev1alpha1.ProfilingRunEvidenceReference{Store: "evidence", Key: "result"} }},
		{name: "measuring", change: func(p *edgev1alpha1.BatchEnergyOptimization, _ *corev1.Node) { p.Status.Phase = "Measuring" }},
		{name: "failed", change: func(p *edgev1alpha1.BatchEnergyOptimization, _ *corev1.Node) { p.Status.Phase = "Failed" }},
		{name: "complete", change: func(p *edgev1alpha1.BatchEnergyOptimization, _ *corev1.Node) { p.Status.Phase = "Complete" }},
		{name: "completed trials", change: func(p *edgev1alpha1.BatchEnergyOptimization, _ *corev1.Node) { p.Status.CompletedTrials = 1 }},
		{name: "outcome exists", change: func(p *edgev1alpha1.BatchEnergyOptimization, _ *corev1.Node) { p.Status.Outcome = "InvalidRunSpec" }},
		{name: "condition exists", change: func(p *edgev1alpha1.BatchEnergyOptimization, _ *corev1.Node) { p.Status.Conditions = []metav1.Condition{{Type: "MeasurementComplete"}} }},
		{name: "observed status", change: func(p *edgev1alpha1.BatchEnergyOptimization, _ *corev1.Node) { p.Status.ObservedGeneration = 1 }},
		{name: "missing generation", change: func(p *edgev1alpha1.BatchEnergyOptimization, _ *corev1.Node) { p.Generation = 0 }},
		{name: "deleting request", change: func(p *edgev1alpha1.BatchEnergyOptimization, _ *corev1.Node) { at := metav1.NewTime(grant); p.DeletionTimestamp = &at; p.Finalizers = []string{"test"} }},
		{name: "no grant", change: func(p *edgev1alpha1.BatchEnergyOptimization, _ *corev1.Node) { p.Annotations = nil }},
		{name: "refused grant", change: func(p *edgev1alpha1.BatchEnergyOptimization, _ *corev1.Node) { p.Annotations[profilingrun.AdmissionAnnotation] = "Refused: Busy" }},
		{name: "malformed grant", change: func(p *edgev1alpha1.BatchEnergyOptimization, _ *corev1.Node) { p.Annotations[profilingrun.AdmissionAnnotation] = "Granted invalid" }},
		{name: "zero grant", change: func(p *edgev1alpha1.BatchEnergyOptimization, _ *corev1.Node) { p.Annotations[profilingrun.AdmissionAnnotation] = profilingrun.GrantedAdmission(time.Time{}) }},
		{name: "missing target", change: func(p *edgev1alpha1.BatchEnergyOptimization, _ *corev1.Node) { p.Spec.State.TargetNode = "" }},
		{name: "missing Node UID", change: func(_ *edgev1alpha1.BatchEnergyOptimization, n *corev1.Node) { n.UID = "" }},
		{name: "deleting Node", change: func(_ *edgev1alpha1.BatchEnergyOptimization, n *corev1.Node) { at := metav1.NewTime(grant); n.DeletionTimestamp = &at; n.Finalizers = []string{"test"} }},
		{name: "no holder", change: func(_ *edgev1alpha1.BatchEnergyOptimization, n *corev1.Node) { n.Spec.Taints = nil }},
		{name: "wrong holder", change: func(_ *edgev1alpha1.BatchEnergyOptimization, n *corev1.Node) { n.Spec.Taints[0].Value = "foreign" }},
		{name: "wrong taint effect", change: func(_ *edgev1alpha1.BatchEnergyOptimization, n *corev1.Node) { n.Spec.Taints[0].Effect = corev1.TaintEffectPreferNoSchedule }},
		{name: "ambiguous holder", change: func(_ *edgev1alpha1.BatchEnergyOptimization, n *corev1.Node) { n.Spec.Taints = append(n.Spec.Taints, n.Spec.Taints[0]) }},
		{name: "canceled", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := &edgev1alpha1.BatchEnergyOptimization{ObjectMeta: metav1.ObjectMeta{
				Namespace: "measurement", Name: strings.Repeat("legacy-", 10), UID: "request-uid", Generation: 1, ResourceVersion: "7",
				Annotations: map[string]string{profilingrun.AdmissionAnnotation: profilingrun.GrantedAdmission(grant)},
			}}
			parent.Spec.State.TargetNode = "node"
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node-uid", ResourceVersion: "11"}, Spec: corev1.NodeSpec{Taints: []corev1.Taint{{Key: profilingrun.MeasurementTaintKey, Effect: corev1.TaintEffectNoSchedule, Value: string(parent.UID)}}}}
			if test.change != nil {
				test.change(parent, node)
			}
			objects := []client.Object{parent, node}
			if test.runOwner != "" {
				controller := test.runOwner != "noncontroller"
				owner := metav1.OwnerReference{APIVersion: edgev1alpha1.GroupVersion.String(), Kind: "BatchEnergyOptimization", Name: parent.Name, UID: parent.UID, Controller: &controller}
				switch test.runOwner {
				case "foreign": owner.UID = "foreign-uid"
				case "version": owner.APIVersion = "other/v1"
				case "kind": owner.Kind = "Other"
				}
				objects = append(objects, &edgev1alpha1.ProfilingRun{ObjectMeta: metav1.ObjectMeta{Name: "unlabeled-run", Namespace: parent.Namespace, OwnerReferences: []metav1.OwnerReference{owner}}})
			}
			scheme := runtime.NewScheme()
			if err := edgev1alpha1.AddToScheme(scheme); err != nil { t.Fatal(err) }
			if err := corev1.AddToScheme(scheme); err != nil { t.Fatal(err) }
			base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			getFailure := test.getFailure
			if getFailure == "parent" { getFailure = parent.Name }
			reader := inspectionReader{Reader: base, getFailure: getFailure, listFailure: test.listError}
			store := &inspectionStore{err: evidencestore.ErrNotFound}
			if test.storeErr != nil { store.err = test.storeErr }
			if test.plan != "" { store.err = nil; store.body = test.plan }
			uid := parent.UID
			if test.wrongUID { uid = "old-uid" }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.cancel { cancel() }
			beforeParent, beforeNode := parent.DeepCopy(), node.DeepCopy()
			proof, err := InspectPretrialFailure(ctx, reader, store, "evidence", client.ObjectKeyFromObject(parent), uid)
			if proof.Eligible != test.eligible || (err == nil) != test.eligible {
				t.Fatalf("eligible=%v error=%v proof=%+v", proof.Eligible, err, proof)
			}
			if strings.Contains(proof.Reason, "sensitive") || (err != nil && strings.Contains(err.Error(), "sensitive")) { t.Fatal("backend details leaked") }
			if test.runOwner == "owned" && proof.OwnedRunCount != 1 { t.Fatalf("owned Run count=%d", proof.OwnedRunCount) }
			if test.eligible {
				if proof.Reason != "InvalidRunSpec" || !proof.PlanNotFound || !proof.PublishedResultAbsent || proof.OwnedRunCount != 0 || !proof.Admission.Granted || !proof.Admission.At.Equal(grant) || proof.Node.UID != node.UID || proof.Node.Holder != string(parent.UID) || proof.Request.ResourceVersion != "7" || proof.Node.ResourceVersion != "11" { t.Fatalf("incomplete proof: %+v", proof) }
				if store.opens != 1 || store.location != curvePlanLocation("evidence", parent.UID) { t.Fatalf("wrong plan authority: %+v", store) }
				encoded, err := json.Marshal(proof)
				if err != nil { t.Fatal(err) }
				var document map[string]json.RawMessage
				if err := json.Unmarshal(encoded, &document); err != nil { t.Fatal(err) }
				for _, field := range []string{"apiVersion", "eligible", "request", "reason", "ownedRunCount", "planNotFound", "publishedResultAbsent", "admission", "node", "checkedAt"} { if _, ok := document[field]; !ok { t.Fatalf("missing JSON field %q", field) } }
				if proof.APIVersion != "chill.dacs.io/pretrial-retirement.v1" || proof.CheckedAt.IsZero() { t.Fatalf("invalid proof envelope: %+v", proof) }
			}
			currentParent := &edgev1alpha1.BatchEnergyOptimization{}
			currentNode := &corev1.Node{}
			if err := base.Get(context.Background(), types.NamespacedName{Namespace: parent.Namespace, Name: parent.Name}, currentParent); err != nil { t.Fatal(err) }
			if err := base.Get(context.Background(), types.NamespacedName{Name: node.Name}, currentNode); err != nil { t.Fatal(err) }
			// Fake clients assign resource versions on creation, before inspection.
			beforeNode.ResourceVersion = currentNode.ResourceVersion
			if !reflect.DeepEqual(beforeParent, currentParent) || !reflect.DeepEqual(beforeNode, currentNode) { t.Fatal("inspection mutated Kubernetes objects") }
		})
	}
}
