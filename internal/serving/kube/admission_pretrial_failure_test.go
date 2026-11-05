package kube

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign"
	campaigncontroller "github.com/lab-paper-code/chill/internal/profiling/campaign/controller"
	"github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	batch "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	ktesting "k8s.io/client-go/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// PretrialFailureObjects exposes only the evidence ports needed by the external
// consumer test. That test imports measurement, which itself imports kube.
type PretrialFailureObjects interface {
	evidencestore.LocationReader
	Delete(context.Context, evidencestore.ObjectLocation) error
}

type pretrialFailurePorts struct {
	reads, writes, resolutions, preparations, results int
}

func (p *pretrialFailurePorts) OpenLocation(context.Context, evidencestore.ObjectLocation) (io.ReadCloser, error) {
	p.reads++
	return nil, evidencestore.ErrNotFound
}

func (p *pretrialFailurePorts) PutImmutable(context.Context, evidencestore.ObjectIdentity, io.Reader) (evidencestore.Receipt, error) {
	p.writes++
	return evidencestore.Receipt{}, errors.New("unexpected evidence write before first trial")
}

func (p *pretrialFailurePorts) Delete(context.Context, evidencestore.ObjectLocation) error { return nil }

func (p *pretrialFailurePorts) ResolveCandidate(context.Context, profiler.CandidateSelection) (profiler.ResolvedCandidate, error) {
	p.resolutions++
	return profiler.ResolvedCandidate{}, errors.New("invalid campaign metadata reached candidate resolution")
}

func (p *pretrialFailurePorts) Prepare(context.Context, profiler.PreparationRequest) (profiler.PreparedRun, error) {
	p.preparations++
	return profiler.PreparedRun{}, errors.New("invalid campaign metadata reached Run preparation")
}

type pretrialFailureClient struct {
	client.Client
	creates int
}

func (c *pretrialFailureClient) Create(ctx context.Context, object client.Object, opts ...client.CreateOption) error {
	c.creates++
	return c.Client.Create(ctx, object, opts...)
}

// ExercisePretrialFailureReturn joins the real admission and campaign paths.
// Kubernetes clients, absent evidence, and the Node agent are local doubles;
// no measurement, derivation, evidence publication, or hardware is executed.
func ExercisePretrialFailureReturn(t *testing.T, deleteMeasurement func(context.Context, client.Client, kubernetes.Interface, PretrialFailureObjects, *edge.BatchEnergyOptimization) error) {
	t.Helper()
	for _, failReturn := range []bool{false, true} {
		name := "return-success"
		if failReturn { name = "return-failure-then-retry" }
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			h := newAdmissionHarness(t)
			// The admission harness normally has no campaign status writer.
			// Register that real API subresource for the campaign reconciler.
			h.nodes.Client = &managerAPI{Client: fake.NewClientBuilder().WithScheme(h.nodes.Scheme()).WithObjects(testExecutionFacts()).
				WithStatusSubresource(&edge.BatchEnergyOptimization{}, &edge.Workload{}, &core.Pod{}, &edge.OperationCostMeasurement{}, &edge.DispatchOccupancyMeasurement{}).Build()}
			class := cpuClass()
			h.create(class)
			h.controller.mu.Lock()
			h.controller.config.PowerPolicies = []nodeactuator.ActuationPolicySpec{{Name: "low", DeviceClass: "cpu", PowerMode: "low", ActuatorKind: string(edge.PowerModeActuatorJetsonNVPModel), SettlingDurationSeconds: 1, MaxActuationDurationSeconds: 60}}
			h.controller.config.Baselines = []serving.NodeBaseline{{ID: "idle", Node: serving.NodeReference{Name: h.node.Name, UID: string(h.node.UID)}, PowerMode: "low", Watts: 5, Meter: "wall", Scope: servingstate.MeasurementWallScope, Reference: evidencestore.ObjectIdentity{Store: "evidence", Key: "idle", Size: 1}}}
			h.controller.mu.Unlock()
			root := beoRoot("pretrial-"+strings.Repeat("campaign-", 8)+"end", h.node.Name, "")
			root.Spec.State.Candidate = edge.ProfilingRunCandidateSpec{ModelSpec: "model", DeviceClass: "cpu", ExecutionPath: "ort-cpu", PowerMode: "max", Preprocessor: "preprocessor"}
			root.Spec.ActiveProtocol = edge.ActiveProtocolSpec{Workload: "workload", Dataset: "dataset", DurationSeconds: 30, MetricObservation: edge.ProfilingRunMetricObservationSpec{IntervalMilliseconds: 1000, TimeoutMilliseconds: 900, DurationSeconds: 31}}
			root.Spec.IdleProtocol = edge.IdleProtocolSpec{SettlingDurationSeconds: 1, DurationSeconds: 30, MetricObservation: root.Spec.ActiveProtocol.MetricObservation}
			root.Spec.TrialsPerBatch, root.Spec.RetryLimit = 3, 2
			root.Spec.BatchSizes = []int32{1, 2, 4, 8, 16, 32}
			if len(validation.IsDNS1123Subdomain(root.Name)) != 0 || len(root.Name) > 200 || len(validation.IsValidLabelValue(root.Name)) == 0 {
				t.Fatal("fixture must have an API-valid name outside the legacy campaign-label contract")
			}
			h.create(root)
			original := root.DeepCopy().Spec
			h.admit()
			if h.holder() != string(root.UID) || !strings.HasPrefix(h.admission(root), "Granted") || root.Status.Phase != "" {
				t.Fatalf("initial admission: holder=%q status=%+v", h.holder(), root.Status)
			}
			ports := &pretrialFailurePorts{}
			if err := deleteMeasurement(ctx, h.controller.store.client, h.kube, ports, root); err != nil { t.Fatal(err) }
			native := &pretrialFailureClient{Client: h.controller.store.client}
			source, err := campaigncontroller.NewCurvePlanSource(native, ports, ports, ports, campaigncontroller.CurvePlanSourceOptions{Store: "evidence", MaxObjectBytes: 1 << 20})
			if err != nil { t.Fatal(err) }
			reconciler, err := campaigncontroller.NewBatchEnergyOptimizationReconciler(native, source.Resolve,
				func(context.Context, derivation.CurvePlan, []edge.ProfilingRun, []edge.ProfilingRun, []int, derivation.EnergyObjectivePolicy) (campaign.CurveResult, error) {
					t.Error("pre-trial failure reached curve derivation")
					return campaign.CurveResult{}, errors.New("unexpected derivation")
				},
				func(context.Context, *edge.BatchEnergyOptimization, campaign.StoredObjectReference, []edge.ProfilingRun, campaign.Result) (campaign.StoredObjectReference, error) {
					ports.results++
					return campaign.StoredObjectReference{}, errors.New("unexpected result publication")
				})
			if err != nil { t.Fatal(err) }
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(root)}
			if _, err = reconciler.Reconcile(ctx, request); err != nil { t.Fatal(err) }
			if err = h.controller.store.client.Get(ctx, request.NamespacedName, root); err != nil { t.Fatal(err) }
			condition := apimeta.FindStatusCondition(root.Status.Conditions, "MeasurementComplete")
			if root.Status.Phase != "Failed" || root.Status.Outcome != profiler.ResolutionReasonInvalidRunSpec || root.Status.ObservedGeneration != root.Generation || condition == nil || condition.Status != meta.ConditionFalse || condition.Reason != root.Status.Outcome || condition.ObservedGeneration != root.Generation || root.Status.Result != nil {
				t.Fatalf("pre-trial failure was not terminal: %+v", root.Status)
			}
			if ports.reads != 1 { t.Fatalf("frozen-plan reads = %d, want 1", ports.reads) }
			version := root.ResourceVersion
			if _, err = reconciler.Reconcile(ctx, request); err != nil { t.Fatal(err) }
			if err = h.controller.store.client.Get(ctx, request.NamespacedName, root); err != nil { t.Fatal(err) }
			if ports.reads != 1 || root.ResourceVersion != version { t.Fatal("terminal replay crossed resolution/status fence") }
			settle := settleAgent(class)
			holderAtReadback := ""
			h.nodes.agent = func(n *core.Node) {
				holderAtReadback, _ = profilingrun.MeasurementHolder(n)
				settle(n)
			}
			if failReturn {
				h.kube.PrependReactor("patch", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
					if failReturn && strings.Contains(string(action.(ktesting.PatchAction).GetPatch()), chillmeta.PowerMode) {
						return true, nil, errors.New("injected lowest-mode request failure")
					}
					return false, nil, nil
				})
				h.admit()
				if h.holder() != string(root.UID) || holderAtReadback != "" { t.Fatal("failed return released taint or fabricated readback") }
				results := h.controller.State().Results
				if len(results) == 0 || results[len(results)-1].Status != servingstate.NativeOperationFailed || !strings.Contains(results[len(results)-1].Error, "injected lowest-mode request failure") { t.Fatalf("return failure not visible: %+v", results) }
				failReturn = false
			}
			h.admit()
			node, err := h.kube.CoreV1().Nodes().Get(ctx, h.node.Name, meta.GetOptions{})
			if err != nil { t.Fatal(err) }
			if holderAtReadback != string(root.UID) || h.holder() != "" || node.Labels[chillmeta.PowerMode] != "low" || node.Labels[chillmeta.PowerModeState] != chillmeta.PowerModeSuccess { t.Fatalf("handback lacked held lowest-mode readback: %+v", node) }
			for _, result := range h.controller.State().Results {
				if result.Operation.Kind == handBackKind && result.Status == servingstate.NativeOperationComplete {
					if !strings.Contains(strings.Join(result.Progress, ";"), "lowest mode low read back;taint removed") { t.Fatalf("handback step order: %v", result.Progress) }
				}
			}
			if err = h.controller.store.client.Get(ctx, request.NamespacedName, root); err != nil { t.Fatal(err) }
			if !reflect.DeepEqual(root.Spec, original) { t.Fatal("campaign spec, including batchSizes, changed before deletion") }
			for _, list := range []client.ObjectList{&edge.ProfilingRunList{}, &edge.ModelLoadedIdleBaselineList{}, &batch.JobList{}, &core.PodList{}} {
				if err = h.controller.store.client.List(ctx, list); err != nil { t.Fatal(err) }
				items, err := apimeta.ExtractList(list)
				if err != nil || len(items) != 0 { t.Fatalf("pre-trial child objects: %T count=%d err=%v", list, len(items), err) }
			}
			for _, action := range h.kube.Actions() {
				if action.GetVerb() == "create" && (action.GetResource().Resource == "pods" || action.GetResource().Resource == "jobs") { t.Fatalf("unexpected measurement actuation: %v", action) }
			}
			if ports.writes != 0 || ports.results != 0 || ports.resolutions != 0 || ports.preparations != 0 { t.Fatalf("pre-trial work crossed boundary: %+v", ports) }
			if native.creates != 0 { t.Fatalf("pre-trial native creates = %d", native.creates) }
			if err = deleteMeasurement(ctx, h.controller.store.client, h.kube, ports, root); err != nil { t.Fatal(err) }
			if err = h.controller.store.client.Get(ctx, request.NamespacedName, &edge.BatchEnergyOptimization{}); !apierrors.IsNotFound(err) { t.Fatalf("normal DELETE retained campaign: %v", err) }
		})
	}
}
