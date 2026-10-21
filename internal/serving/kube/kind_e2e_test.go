//go:build e2e

package kube

// These tests run the ChillServer scheduler controller against the Kubernetes
// API of a repository-local kind cluster: Nodes, Pods, Services, the
// measurement taint and Pod-owning Workloads are real API objects. Measurement
// roots, profiles, meters, power, native state and kubelet logs are fakes.
// Run them with make test-e2e.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/yaml"
)

// kindConfig returns the client configuration of KUBECONFIG and fails unless
// its current context is kind-*.
func kindConfig(t *testing.T) *rest.Config {
	t.Helper()
	path := os.Getenv("KUBECONFIG")
	if path == "" || strings.Contains(path, string(os.PathListSeparator)) {
		t.Fatalf("KUBECONFIG must name one kind kubeconfig, got %q", path)
	}
	raw, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw.CurrentContext, "kind-") {
		t.Fatalf("refusing kube context %q; expected kind-*", raw.CurrentContext)
	}
	config, err := clientcmd.NewDefaultClientConfig(*raw, nil).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func kindKube(t *testing.T) kubernetes.Interface {
	t.Helper()
	kube, err := kubernetes.NewForConfig(kindConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	return kube
}

// kindWorkload creates a Workload object in kind, installing the repository
// Workload CRD when kind lacks it, so the garbage collector keeps the Pods it
// owns. It returns the server-assigned UID.
func kindWorkload(t *testing.T, name string) string {
	t.Helper()
	ctx := context.Background()
	client, err := dynamic.NewForConfig(kindConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../../config/crd/bases/edge.dacs.io_workloads.yaml")
	if err != nil {
		t.Fatal(err)
	}
	crd := &unstructured.Unstructured{}
	if err = yaml.Unmarshal(data, &crd.Object); err != nil {
		t.Fatal(err)
	}
	crds := client.Resource(schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"})
	if _, err = crds.Create(ctx, crd, meta.CreateOptions{}); err == nil {
		t.Cleanup(func() { _ = crds.Delete(ctx, crd.GetName(), meta.DeleteOptions{}) })
	} else if !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	workloads := client.Resource(edge.GroupVersion.WithResource("workloads"))
	workload := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": edge.GroupVersion.String(), "kind": "Workload", "metadata": map[string]any{"name": name},
		"spec": map[string]any{"modelRef": map[string]any{"name": "model"}, "preprocessorRef": map[string]any{"name": "pre"},
			"slo": map[string]any{"percentile": int64(99), "targetLatency": "1s"}},
	}}
	// The new CRD serves once it is established.
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		created, err := workloads.Create(ctx, workload, meta.CreateOptions{})
		if err == nil {
			t.Cleanup(func() { _ = workloads.Delete(ctx, name, meta.DeleteOptions{}) })
			return string(created.GetUID())
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
	}
}

// kindNamespace creates namespace unless it exists and deletes it afterwards
// only when this test created it.
func kindNamespace(t *testing.T, kube kubernetes.Interface, name string) {
	t.Helper()
	ctx := context.Background()
	_, err := kube.CoreV1().Namespaces().Create(ctx, &core.Namespace{ObjectMeta: meta.ObjectMeta{Name: name}}, meta.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = kube.CoreV1().Namespaces().Delete(ctx, name, meta.DeleteOptions{}) })
}

// kindKubelet serves the kubelet container log endpoint for kubelet-less
// Nodes: every stream stays open and empty. It listens on the host address
// of the kind network and returns the Node status that points at it.
func kindKubelet(t *testing.T, kube kubernetes.Interface) (core.NodeAddress, core.NodeDaemonEndpoints) {
	t.Helper()
	nodes, err := kube.CoreV1().Nodes().List(context.Background(), meta.ListOptions{LabelSelector: controlPlaneLabel})
	if err != nil || len(nodes.Items) == 0 {
		t.Fatalf("kind control plane: %v", err)
	}
	var plane net.IP
	for _, a := range nodes.Items[0].Status.Addresses {
		if a.Type == core.NodeInternalIP {
			plane = net.ParseIP(a.Address)
		}
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	host := ""
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.Contains(plane) && !n.IP.Equal(plane) {
			host = n.IP.String()
		}
	}
	if host == "" {
		t.Fatalf("no host address on the kind network of %s", plane)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	server.Listener = listener
	server.StartTLS()
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})
	return core.NodeAddress{Type: core.NodeInternalIP, Address: host},
		core.NodeDaemonEndpoints{KubeletEndpoint: core.DaemonEndpoint{Port: int32(listener.Addr().(*net.TCPAddr).Port)}}
}

// kindNode registers a Ready Node shaped like the observer fixture, backed by
// kindKubelet, and returns it with its server-assigned UID. It renews the
// Ready heartbeat in place of a kubelet so the node controller keeps it Ready.
func kindNode(t *testing.T, kube kubernetes.Interface, name string) *core.Node {
	t.Helper()
	ctx := context.Background()
	_, fixture, _, _ := observerSourceFixture()
	fixture.Name, fixture.UID = name, ""
	address, endpoints := kindKubelet(t, kube)
	fixture.Status.Addresses, fixture.Status.DaemonEndpoints = []core.NodeAddress{address}, endpoints
	_, err := kube.CoreV1().Nodes().Create(ctx, fixture, meta.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = kube.CoreV1().Nodes().Delete(ctx, name, meta.DeleteOptions{}) })
	heartbeat := func() (current *core.Node, err error) {
		// The node controller writes the Node too.
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if current, err = kube.CoreV1().Nodes().Get(ctx, name, meta.GetOptions{}); err != nil {
				return err
			}
			current.Status = *fixture.Status.DeepCopy()
			current.Status.Conditions[0].LastHeartbeatTime = meta.Now()
			current, err = kube.CoreV1().Nodes().UpdateStatus(ctx, current, meta.UpdateOptions{})
			return err
		})
		return current, err
	}
	node, err := heartbeat()
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Second):
				_, _ = heartbeat()
			}
		}
	}()
	return node
}

// kindPod binds a Pod to node directly; without a kubelet it stays Pending.
func kindPod(t *testing.T, kube kubernetes.Interface, namespace, name, node string, labels map[string]string) *core.Pod {
	t.Helper()
	ctx := context.Background()
	pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Namespace: namespace, Name: name, Labels: labels},
		Spec: core.PodSpec{NodeName: node, Containers: []core.Container{{Name: "main", Image: name + "-image"}}}}
	var created *core.Pod
	var err error
	// The namespace's default ServiceAccount may appear after the namespace.
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if created, err = kube.CoreV1().Pods(namespace).Create(ctx, pod, meta.CreateOptions{}); err == nil || time.Now().After(deadline) {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = kube.CoreV1().Pods(namespace).Delete(ctx, name, meta.DeleteOptions{GracePeriodSeconds: new(int64)})
	})
	return created
}

func kindHolder(t *testing.T, kube kubernetes.Interface, name string) string {
	t.Helper()
	node, err := kube.CoreV1().Nodes().Get(context.Background(), name, meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	value, _ := profilingrun.MeasurementHolder(node)
	return value
}

// SCH-AC-26: an available Node moves to the measurement POOL and returns, a
// serving Node is refused with its reason, and a Node with a Pod CHILL did not
// place is measured and that Pod is recorded.
func TestE2EKindSCHAC26MeasurementAdmission(t *testing.T) {
	kube := kindKube(t)
	ctx := context.Background()
	suffix := rand.String(5)
	kindNamespace(t, kube, "chill-system")
	available := kindNode(t, kube, "chill-e2e-available-"+suffix)
	servingNode := kindNode(t, kube, "chill-e2e-serving-"+suffix)
	externalNode := kindNode(t, kube, "chill-e2e-external-"+suffix)
	servingInstance := kindPod(t, kube, "chill-system", "chill-e2e-instance-"+suffix, servingNode.Name,
		map[string]string{"app.kubernetes.io/component": servingInstanceComponent})
	external := kindPod(t, kube, "default", "chill-e2e-external-"+suffix, externalNode.Name, map[string]string{"app": "external"})

	controller, _ := newAdmissionController(t, kube)
	h := &admissionHarness{t: t, controller: controller}
	roots := map[string]*core.Node{"available": available, "serving": servingNode, "external": externalNode}
	created := time.Now()
	for name, node := range roots {
		root := baselineRoot(name, created)
		root.Spec.State.TargetNode = node.Name
		h.create(root)
	}
	admit := func() {
		t.Helper()
		if err := h.controller.Admit(ctx); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			busy := false
			for _, node := range roots {
				busy = busy || h.controller.nodeBusy(node)
			}
			if !busy {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("run did not finish")
			}
		}
	}
	admit()

	for _, name := range []string{"available", "external"} {
		root := baselineRoot(name, created)
		if granted, _, _ := profilingrun.Admission(map[string]string{profilingrun.AdmissionAnnotation: h.admission(root)}); !granted {
			t.Fatalf("%s: measurement not granted", name)
		}
		if holder := kindHolder(t, kube, roots[name].Name); holder != string(root.UID) {
			t.Fatalf("%s: taint holder = %q", name, holder)
		}
	}
	want := "Refused: NodeServing chill-system/" + servingInstance.Name
	if got := h.admission(baselineRoot("serving", created)); got != want {
		t.Fatalf("serving admission = %q, want %q", got, want)
	}
	if holder := kindHolder(t, kube, servingNode.Name); holder != "" {
		t.Fatalf("serving Node tainted by %q", holder)
	}

	state, err := h.controller.operations.Capture(ctx, serving.NodeReference{Name: externalNode.Name, UID: string(externalNode.UID)})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Pods) != 1 || state.Pods[0].UID != string(external.UID) || state.Pods[0].Namespace != "default" || state.Pods[0].Name != external.Name {
		t.Fatalf("recorded Pods = %+v", state.Pods)
	}
	if _, err = kube.CoreV1().Pods("default").Get(ctx, external.Name, meta.GetOptions{}); err != nil {
		t.Fatalf("external Pod touched: %v", err)
	}

	h.complete(baselineRoot("available", created))
	// The taint patch carries the read resourceVersion; a Node status write
	// between read and patch defers the hand-back to the next cycle.
	cycles := 0
	for holder := "-"; holder != ""; holder = kindHolder(t, kube, available.Name) {
		if cycles++; cycles > 5 {
			t.Fatalf("available Node not returned, taint holder %q", holder)
		}
		admit()
	}
	t.Logf("granted %s and %s, refused %s, recorded %s/%s, returned %s after %d cycles",
		available.Name, externalNode.Name, servingNode.Name, external.Namespace, external.Name, available.Name, cycles)
}

// kindScheduler builds a scheduler controller on kind with the workloads
// published, the prepared and other profiles completed and a measured
// preparation for each prepared profile on its Node. Baselines, prepare
// costs and dispatch occupancy are fake wall readings.
func kindScheduler(t *testing.T, kube kubernetes.Interface, autonomous []string, prepared, others []*serving.Profile, nodes []*core.Node, workloads ...*serving.Definition) *SchedulerController {
	t.Helper()
	controller, _ := newAdmissionController(t, kube)
	_, _, _, class := observerSourceFixture()
	eco := edge.PowerModeActuator{Kind: edge.PowerModeActuatorJetsonNVPModel, JetsonNVPModel: &edge.JetsonNVPModelTarget{ID: 1}}
	class.Spec.PowerModes = append(class.Spec.PowerModes, edge.PowerMode{Name: "eco", Actuator: eco})
	if err := controller.store.client.Create(context.Background(), &class); err != nil {
		t.Fatal(err)
	}
	for _, d := range workloads {
		if err := controller.runtime.Publish(d); err != nil {
			t.Fatal(err)
		}
	}
	completed := make([]serving.Profile, 0, len(prepared)+len(others))
	for _, p := range append(slices.Clone(prepared), others...) {
		completed = append(completed, *p)
	}
	controller.observer.SetProfiles(completed)
	config := controller.config
	config.Settings.AutonomousWorkloads = autonomous
	config.Settings.MaxInstances = 4
	// kind API reads and the capacity rows share one host; a kubelet-less
	// Node is observed only as fresh as this test's capture.
	config.Settings.MaxObservationAge = 30 * time.Second
	config.Settings.Controls = serving.SchedulerControls{Placement: true, Resources: true, Replicas: true, Routing: true, Batching: true, Power: true, Membership: true}
	config.PowerPolicies = []nodeactuator.ActuationPolicySpec{{Name: "cpu-eco", DeviceClass: "cpu", PowerMode: "eco",
		ActuatorKind: nodeactuator.ActuatorKindJetsonNVPModel, SettlingDurationSeconds: 1, MaxActuationDurationSeconds: 30}}
	config.Preparations = map[string]PrepareSpec{}
	for _, profile := range prepared {
		spec := measuredCPUPreparationFixture()
		spec.Instance.Profile = profile
		spec.Instance.Node = serving.NodeReference{Name: profile.NodeName, UID: profile.Condition.NodeUID}
		spec.Instance.MaxBatch, spec.Instance.Capacity = 1, 4
		spec.Instance.IdleMeter, spec.Instance.IdleScope = "wall-"+profile.NodeName, "node-exclusive-wall"
		config.Preparations[profile.ID] = spec
		c := profile.Condition
		config.DispatchOccupancy = append(config.DispatchOccupancy, serving.DispatchOccupancy{Node: spec.Instance.Node, PowerMode: c.PowerMode, RuntimeImage: c.RuntimeImage, Model: c.LogicalModel, Source: "fake"})
		config.Costs = append(config.Costs, serving.OperationCost{ID: "prepare-" + profile.ID, Node: spec.Instance.Node, Kind: operationPrepare, From: "unloaded",
			To: serving.SchedulerInstanceState(spec.Instance), PowerMode: "fixed",
			Trials: []serving.OperationTrial{{Duration: time.Millisecond, Joules: 0.01}}, EnergyScope: "whole-wall", Meter: "wall-" + profile.NodeName,
			Scope: "node-exclusive-wall", Reference: evidencestore.ObjectIdentity{Store: "raw", Key: "prepare-" + profile.ID, Size: 1}})
	}
	for _, n := range nodes {
		config.Meters = append(config.Meters, MeterDeclaration{ID: "wall-" + n.Name, Node: serving.NodeReference{Name: n.Name, UID: string(n.UID)}, Scope: measurementWallScope,
			Source: kindMeterSource(n.Name)})
		config.Baselines = append(config.Baselines, serving.NodeBaseline{ID: "idle-" + n.Name, Node: serving.NodeReference{Name: n.Name, UID: string(n.UID)},
			PowerMode: "fixed", Watts: 10, Meter: "wall-" + n.Name, Scope: "node-exclusive-wall", MeasuredAt: time.Now(),
			Reference: evidencestore.ObjectIdentity{Store: "raw", Key: "idle-" + n.Name, Size: 1}})
	}
	if err := controller.SetConfiguration(config); err != nil {
		t.Fatal(err)
	}
	return controller
}

// kindMeterSource is the fake wall series of node.
func kindMeterSource(node string) metricobserver.SourceIdentity {
	return metricobserver.SourceIdentity{NodeName: node, Endpoint: "fake://" + node, Metric: "wall"}
}

const kindExecutionPath = "cpu"

// kindProfile is a completed measurement of base on node for model.
func kindProfile(base serving.Profile, node *core.Node, model string) *serving.Profile {
	profile := base
	profile.NodeName, profile.Condition.NodeUID, profile.MetricSource = node.Name, string(node.UID), kindMeterSource(node.Name)
	profile.Condition.LogicalModel = model
	profile.Condition.Preprocessor.Name = "pre"
	profile.Condition.Preprocessor.Inputs, profile.Condition.Preprocessor.Outputs = []string{"x"}, []string{"y"}
	profile.Condition.ActiveProtocol.Dataset = &profilederivation.CurveDatasetContract{Name: "data", UID: "data-uid"}
	profile.Condition.Artifact.Name, profile.Condition.Artifact.ExecutionPath = "model", kindExecutionPath
	profile.MeasuredAt = time.Now()
	profile.Reference = evidencestore.ObjectIdentity{Store: "raw", Key: "curve-" + model, Size: 1}
	profile.Evidence = []evidencestore.ObjectIdentity{{Store: "raw", Key: "profile-" + model, Size: 1}}
	profile.Batches = []serving.BatchProfile{{Batch: 1, DurationNanos: []float64{float64(10 * time.Millisecond)}, EnergyPerInputJoules: 1, MemoryBytes: 1024}}
	return &profile
}

// measuredProfile is the measured CPU preparation's profile on node.
func measuredProfile(node *core.Node) *serving.Profile {
	profile := kindProfile(*measuredCPUPreparationFixture().Instance.Profile, node, "model")
	profile.ID = "measured-" + node.Name
	return profile
}

// kindDefinition is a Workload of model that kindProfile of model matches.
func kindDefinition(uid, model string, profile *serving.Profile) *serving.Definition {
	c := profile.Condition
	return &serving.Definition{Name: "workload-" + uid, UID: uid, Generation: 1,
		SLO:          serving.SLO{Percentile: 99, TargetLatency: time.Second},
		Dataset:      &serving.DatasetReference{Name: "data", UID: "data-uid"},
		Preprocessor: serving.Preprocessor{Name: "pre", Input: []serving.RawField{{Name: "x", Format: "UTF8", MaxBytes: 16}}, Output: []serving.RawField{{Name: "y", Format: "UTF8"}}},
		Model: serving.Model{Name: model, UID: model + "-uid", Generation: 1, Paths: []serving.ModelPath{{Name: kindExecutionPath, Artifact: "model",
			Files: c.Artifact.Files, Image: c.Artifact.Image, RuntimeFamily: c.RuntimeFamily, Backend: c.Backend, Firstfloor: c.Firstfloor,
			Preprocessor: serving.PreprocessorArtifact{Image: c.Preprocessor.Image, Architecture: c.Preprocessor.Architecture, Path: c.Preprocessor.Path}}}},
	}
}

// kindDecide ticks after one observation window until the capacity rows of
// the controlled Workloads are built.
func kindDecide(t *testing.T, c *SchedulerController) serving.SchedulerDecision {
	t.Helper()
	time.Sleep(c.Settings().ObservationWindow)
	for deadline := time.Now().Add(120 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		d, err := c.Tick(context.Background(), time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if d.Status != serving.CapacityPending {
			return d
		}
		if time.Now().After(deadline) {
			t.Fatalf("capacity rows not built: %+v %+v", d.Reasons, c.Capacity())
		}
	}
}

func kindPrepares(c *serving.SchedulerCandidate, node *core.Node) bool {
	return c != nil && slices.ContainsFunc(c.Actions, func(a serving.Action) bool {
		return a.Kind == operationPrepare && a.Node.UID == string(node.UID)
	})
}

func kindWorkloadState(d serving.SchedulerDecision, uid string) serving.SchedulerWorkloadState {
	for _, s := range d.Workloads {
		if s.UID == uid {
			return s
		}
	}
	return serving.SchedulerWorkloadState{}
}

func kindLog(t *testing.T, d serving.SchedulerDecision) {
	t.Helper()
	actions := []string{}
	if d.Target != nil {
		for _, a := range d.Target.Actions {
			actions = append(actions, a.Kind+"@"+a.Node.Name)
		}
	}
	t.Logf("status=%s reasons=%v keep=%s target actions=%v workloads=%+v", d.Status, d.Reasons, d.Keep.Status, actions, d.Workloads)
	for _, i := range d.Keep.Instances {
		t.Logf("instance %s node=%s owner=%q", i.ID, i.Node.Name, i.OwnerWorkloadUID)
	}
	if d.Capacity != nil {
		for _, p := range d.Capacity.Plans {
			t.Logf("plan node=%s key=%s eligible=%v reason=%s", p.Node.Name, p.Key, p.Eligible, p.Reason)
		}
	}
}

// SCH-AC-24: a Workload whose only completed measurement is on a Node that
// never served it starts on that Node; the other available Node keeps its
// lowest mode, the configured completion margin is evaluated and the next
// control interval decides again.
func TestE2EKindSCHAC24FirstConfiguration(t *testing.T) {
	kube := kindKube(t)
	suffix := rand.String(5)
	measured := kindNode(t, kube, "chill-e2e-measured-"+suffix)
	other := kindNode(t, kube, "chill-e2e-other-"+suffix)
	profile := measuredProfile(measured)
	c := kindScheduler(t, kube, []string{"w-on"}, []*serving.Profile{profile}, nil, []*core.Node{measured, other}, kindDefinition("w-on", "model", profile))
	config := c.config
	config.Settings.CompletionMarginMS = 139
	if err := c.SetConfiguration(config); err != nil {
		t.Fatal(err)
	}
	first := kindDecide(t, c)
	kindLog(t, first)
	prepares := 0
	if first.Target != nil {
		for _, a := range first.Target.Actions {
			if a.Kind == operationPrepare {
				prepares++
			}
		}
	}
	if len(first.Keep.Instances) != 0 || prepares != 1 || !kindPrepares(first.Target, measured) {
		t.Fatalf("first configuration is not one instance on the measured Node: %+v", first.Target)
	}
	for _, n := range first.Target.Nodes {
		if n.Node.UID == string(other.UID) && n.PowerMode != serving.SchedulerLowestMode(c.config.Baselines, n.Node) {
			t.Fatalf("other available Node mode %q", n.PowerMode)
		}
	}
	rows := c.Capacity()
	if first.Settings.CompletionMarginMS != 139 || len(rows) == 0 || rows[0].Input.CompletionMarginMS != 139 || rows[0].Kappa == nil {
		t.Fatalf("capacity not evaluated with the completion margin: %+v", rows)
	}
	t.Logf("capacity row %s κ=%v margin=%vms built in %s", rows[0].Status, *rows[0].Kappa, rows[0].Input.CompletionMarginMS, rows[0].Elapsed)
	next := kindDecide(t, c)
	kindLog(t, next)
	if !next.At.After(first.At) || kindWorkloadState(next, "w-on").State != serving.WorkloadControlled {
		t.Fatalf("next control interval did not decide again: %+v", next.Workloads)
	}
}

// kindInstance places the observer fixture's serving instance of profile,
// owned by Workload owner, on node: a kind Pod with its native Service and
// container status, and a fake native state endpoint behind an HTTP proxy.
func kindInstance(t *testing.T, kube kubernetes.Interface, c *SchedulerController, node *core.Node, profile *serving.Profile, owner *serving.Definition) {
	t.Helper()
	ctx := context.Background()
	fixture, _, _, _ := observerSourceFixture()
	pod := fixture.DeepCopy()
	pod.Name, pod.UID, pod.Spec.NodeName = "chill-e2e-fixed-"+rand.String(5), "", node.Name
	pod.Annotations[ServingProfileAnnotation] = profile.ID
	pod.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", Name: owner.Name, UID: types.UID(owner.UID)}}
	delete(pod.Labels, instancePodUIDLabel)
	created, err := kube.CoreV1().Pods(pod.Namespace).Create(ctx, pod, meta.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = kube.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, meta.DeleteOptions{GracePeriodSeconds: new(int64)})
	})
	pods := kube.CoreV1().Pods(pod.Namespace)
	// Kubernetes controllers write the new Pod too.
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if created, err = pods.Get(ctx, pod.Name, meta.GetOptions{}); err != nil {
			return err
		}
		created.Labels[instancePodUIDLabel] = string(created.UID)
		if created, err = pods.Update(ctx, created, meta.UpdateOptions{}); err != nil {
			return err
		}
		created.Status = fixture.Status
		created, err = pods.UpdateStatus(ctx, created, meta.UpdateOptions{})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	service := instanceService(created)
	if _, err = kube.CoreV1().Services(service.Namespace).Create(ctx, service, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = kube.CoreV1().Services(service.Namespace).Delete(ctx, service.Name, meta.DeleteOptions{}) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/state") {
			http.NotFound(w, r)
			return
		}
		actual := *profile.RuntimeMetadata
		actual.ModelFiles = deliveredModelFiles(profile.Condition.Artifact.Files)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(native.State{At: time.Now().UTC(), Ready: true, Metadata: native.Metadata{
			Model: "chill_model", Mode: "serving", ProfileID: profile.ID, AppliedAt: time.Now().UTC(),
			Runtime: actual, MaxQueueRequests: 4, MaxBatchSize: 1, ModelInstanceCount: 1,
		}})
	}))
	t.Cleanup(server.Close)
	proxy, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.observer.native.HTTPClient = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
}

// SCH-AC-25: a Workload with autonomous control off keeps its instance and
// the mode of its Node, an autonomous Workload without a completed
// measurement is marked as waiting for evidence, and the measured autonomous
// Workload is still decided.
func TestE2EKindSCHAC25Coexistence(t *testing.T) {
	kube := kindKube(t)
	suffix := rand.String(5)
	kindNamespace(t, kube, "chill-system")
	measured := kindNode(t, kube, "chill-e2e-measured-"+suffix)
	fixed := kindNode(t, kube, "chill-e2e-fixed-"+suffix)
	profile := measuredProfile(measured)
	_, _, base, _ := observerSourceFixture()
	base.RuntimeMetadata.RuntimeVersion = "kind-runtime"
	base.RuntimeMetadata.InitializedBatches = []int{1}
	off := kindProfile(base, fixed, "off-model")
	off.RuntimeMetadata.Inputs, off.RuntimeMetadata.Outputs = off.Condition.Preprocessor.Inputs, off.Condition.Preprocessor.Outputs
	name := "chill-e2e-off-" + suffix
	disabled := kindDefinition(kindWorkload(t, name), "off-model", off)
	disabled.Name = name
	onFixed := measuredProfile(fixed)
	c := kindScheduler(t, kube, []string{"w-on", "w-noev"}, []*serving.Profile{profile, onFixed}, []*serving.Profile{off}, []*core.Node{measured, fixed},
		kindDefinition("w-on", "model", profile), kindDefinition("w-noev", "unmeasured-model", profile), disabled)
	kindInstance(t, kube, c, fixed, off, disabled)
	d := kindDecide(t, c)
	kindLog(t, d)
	for uid, want := range map[string]string{"w-on": serving.WorkloadControlled, "w-noev": serving.WorkloadMeasurementEvidenceMissing, disabled.UID: serving.WorkloadDisabled} {
		if got := kindWorkloadState(d, uid).State; got != want {
			t.Fatalf("%s state %q, want %q", uid, got, want)
		}
	}
	held := slices.IndexFunc(d.Keep.Instances, func(i serving.SchedulerInstance) bool {
		return i.WorkloadUID == disabled.UID || i.OwnerWorkloadUID == disabled.UID
	})
	if held < 0 || d.Keep.Instances[held].Node.UID != string(fixed.UID) {
		t.Fatalf("disabled Workload instance not captured on its Node: %+v", d.Keep.Instances)
	}
	instance := d.Keep.Instances[held]
	if d.Target == nil || !kindPrepares(d.Target, measured) {
		t.Fatalf("measured Workload not decided: %+v", d.Target)
	}
	if !slices.ContainsFunc(d.Target.Instances, func(i serving.SchedulerInstance) bool { return i.ID == instance.ID && i.Node == instance.Node }) ||
		slices.ContainsFunc(d.Target.Actions, func(a serving.Action) bool { return a.Instance == instance.ID || a.Node.UID == string(fixed.UID) }) {
		t.Fatalf("target changed the disabled Workload's instance or Node: %+v", d.Target.Actions)
	}
	if d.Capacity == nil || !slices.ContainsFunc(d.Capacity.Plans, func(p serving.SchedulerNodePlan) bool {
		return p.Node.UID == string(fixed.UID) && p.Reason == "FixedWorkload"
	}) {
		t.Fatalf("decision did not record FixedWorkload on %s", fixed.Name)
	}
}
