package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/kubeconfig"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestMeasurementMembershipIsolation(t *testing.T) {
	const isolated = "measurement-campaign"
	cases := []struct {
		name, kind, source, target, uid string
		allowed                         bool
	}{
		{"join detached node", operationMembership, "", isolated, "node-uid", true},
		{"detach isolated node", operationMembership, isolated, "", "node-uid", true},
		{"reject detached no-op", operationMembership, "", "", "node-uid", false},
		{"reject attached no-op", operationMembership, isolated, isolated, "node-uid", false},
		{"idle isolated node", "idle", isolated, "", "node-uid", true},
		{"reject detached idle", "idle", "", "", "node-uid", false},
		{"preserve another source pool", operationMembership, "live-pool", isolated, "node-uid", false},
		{"preserve another measurement pool", operationMembership, "measurement-other", isolated, "node-uid", false},
		{"reject another target pool", operationMembership, isolated, "measurement-other", "node-uid", false},
		{"reject replaced physical node", operationMembership, "", isolated, "old-uid", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kube := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name: "node", UID: "node-uid", Labels: map[string]string{servingkube.ServingPoolLabel: tc.source},
			}})
			manifest := commandManifest{IsolatedPool: isolated}
			manifest.Measurement.Operation = servingkube.Operation{
				Kind: tc.kind, Node: serving.NodeReference{Name: "node", UID: tc.uid}, Pool: tc.target,
			}
			pool, err := validateMeasurementNode(context.Background(), kube, manifest)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%t, error=%v", tc.allowed, err)
			}
			if tc.allowed && pool != tc.source {
				t.Fatalf("initial isolation pool=%q, actual source=%q", pool, tc.source)
			}
		})
	}
}

func executionOwnerFixture() (executionManifest, executionConfiguration, executionOwnerView, time.Time) {
	now := time.Unix(100, 0)
	routing := map[string]map[string]int{"relaxed": {"source": 1}, "tight": {"source": 2}}
	actual := map[string]map[string]int32{"relaxed": {"source": 1}, "tight": {"source": 2}}
	profile := &serving.Profile{ID: "profile", Condition: derivation.CurvePlanSpec{LogicalModel: "model"}}
	first := &serving.Definition{
		Name: "relaxed", UID: "relaxed", Model: serving.Model{Name: "model", Paths: []serving.ModelPath{{}}},
	}
	second := *first
	second.Name, second.UID = "tight", "tight"
	m := executionManifest{
		SourceConfigRV: "10", Operation: servingkube.Operation{Kind: operationRemove, Instance: "replica"},
	}
	config := executionConfiguration{StaticRouting: routing}
	owner := executionOwnerView{
		Settings: serving.SchedulerSettings{
			Algorithm: serving.SchedulerAlgorithmFixedHorizon, ControlInterval: time.Second,
		},
		State: servingkube.SchedulerControllerState{
			Snapshot: serving.SchedulerSnapshot{
				At: now.Add(-time.Millisecond), Workloads: []*serving.Definition{first, &second},
				Instances: []serving.SchedulerInstance{{ID: "source", Profile: profile, Ready: true}}, Routing: actual,
			},
			Results: []servingkube.OperationResult{{
				Operation: servingkube.Operation{Kind: manualStaticRouting, SourceConfigRV: "10", StaticRouting: routing},
				Actual:    servingkube.OperationState{Routing: actual},
				Status:    operationComplete, EndedAt: now.Add(-time.Second / 2),
			}},
		},
	}
	return m, config, owner, now
}

func TestExecuteRequiresOwnerAcknowledgementAndFullResponseDrain(t *testing.T) {
	for _, name := range []string{
		"safe", "returning", "assigned", "routed", "pre-ack snapshot", "before acknowledgement", "wrong revision",
		"wrong actual", "missing UID", "desired controls", "live controls", "running",
		"failed acknowledgement", "occupied membership", "future snapshot",
	} {
		t.Run(name, func(t *testing.T) {
			m, config, owner, now := executionOwnerFixture()
			acknowledged := owner.State.Results[0]
			acknowledged.EndedAt = now.Add(-3 * time.Second)
			owner.State.Snapshot.At = now.Add(-2 * time.Second)
			// Runtime result retention can expire; the durable record remains the authority.
			owner.State.Results = nil
			switch name {
			case "returning", "assigned":
				owner.State.Snapshot.Outstanding = []serving.SchedulerRequest{{Instance: "replica", Phase: name}}
			case "routed":
				m.Operation.Instance = "source"
			case "pre-ack snapshot":
				owner.State.Snapshot.At = acknowledged.EndedAt.Add(-time.Millisecond)
			case "before acknowledgement":
				owner.State.Snapshot.At = acknowledged.EndedAt
			case "future snapshot":
				owner.State.Snapshot.At = now.Add(time.Millisecond)
			case "wrong revision":
				m.SourceConfigRV = "11"
			case "wrong actual":
				acknowledged.Actual.Routing = map[string]map[string]int32{"relaxed": {"source": 1}}
			case "missing UID":
				delete(config.StaticRouting, "tight")
			case "desired controls":
				config.Settings.Controls.Enabled = true
			case "live controls":
				owner.Settings.Controls.Enabled = true
			case "running":
				owner.State.Actions = []serving.Action{{Status: "running"}}
			case "failed acknowledgement":
				acknowledged.Status = operationFailed
			case "occupied membership":
				m.Operation.Kind = operationMembership
			}
			err := validateExecutionOwner(m, config, owner, &acknowledged, now)
			if (err == nil) != (name == "safe") {
				t.Fatalf("unexpected owner safety verdict: %v", err)
			}
		})
	}
}

func TestExecuteOwnerAllowsAgedPostAckSnapshot(t *testing.T) {
	for _, kind := range []string{operationPrepare, operationMembership, operationRemove} {
		t.Run(kind, func(t *testing.T) {
			m, config, owner, now := executionOwnerFixture()
			m.Operation = servingkube.Operation{
				Kind: kind, Node: serving.NodeReference{Name: "empty", UID: "empty-uid"},
			}
			if kind == operationRemove {
				m.Operation.Instance = "replica"
			}
			acknowledged := owner.State.Results[0]
			acknowledged.EndedAt = now.Add(-3 * time.Second)
			owner.State.Snapshot.At = now.Add(-2 * time.Second)
			err := validateExecutionOwner(m, config, owner, &acknowledged, now)
			if err != nil {
				t.Fatalf("aged closed-ingress post-ack snapshot refused: %v", err)
			}
		})
	}
}

func TestExecuteNodeProtectsServingMembershipAndExactSource(t *testing.T) {
	const daemonCase = "daemon"
	const exactRemoveCase = "exact remove"
	const servingDaemonCase = "serving daemon"
	const foreignRemoveCase = "foreign remove"
	const changedContainerCase = "changed container"
	const foreignComponentCase = "foreign component collision"
	for _, name := range []string{
		"empty join", "empty rollback", "foreign namespace", daemonCase, servingDaemonCase, "changed UID",
		"changed pool", "foreign destination", exactRemoveCase, foreignRemoveCase, changedContainerCase, foreignComponentCase,
	} {
		t.Run(name, func(t *testing.T) {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "target", UID: "node-uid"}}
			spec := servingkube.PrepareSpec{Name: "measured", Instance: serving.SchedulerInstance{
				ID: "slot", Node: serving.NodeReference{Name: "target", UID: "node-uid"}, Pool: "live",
			}}
			m := executionManifest{Namespace: "own", ProfileID: "profile",
				Operation: servingkube.Operation{Kind: operationMembership, Node: spec.Instance.Node, Pool: "live"},
			}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Namespace: "foreign", Name: "busy"}, Spec: corev1.PodSpec{NodeName: "target"},
			}
			switch name {
			case "empty rollback":
				node.Labels = map[string]string{servingkube.ServingPoolLabel: "live"}
				m.ExpectedPool, m.Operation.Pool = "live", ""
			case "changed UID":
				node.UID = "replacement"
			case "changed pool":
				node.Labels = map[string]string{servingkube.ServingPoolLabel: "other"}
			case "foreign destination":
				m.Operation.Pool = "other"
			case foreignComponentCase:
				pod.Labels = map[string]string{"app.kubernetes.io/component": "serving-instance"}
			case daemonCase, servingDaemonCase:
				controller := true
				pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Controller: &controller}}
				if name == servingDaemonCase {
					pod.Namespace = m.Namespace
					pod.Labels = map[string]string{"app.kubernetes.io/component": "serving-instance"}
				}
			case exactRemoveCase, foreignRemoveCase, changedContainerCase:
				node.Labels = map[string]string{servingkube.ServingPoolLabel: "live"}
				m.ExpectedPool, m.Operation.Kind = "live", operationRemove
				m.Operation.Instance = "pod-uid/containerd://exact"
				pod.Namespace, pod.Name, pod.UID = "own", "measured", "pod-uid"
				pod.Annotations = map[string]string{
					servingkube.ServingProfileAnnotation: "profile", servingkube.ServingSlotAnnotation: "slot",
				}
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "firstfloor", ContainerID: "containerd://exact"}}
				if name == foreignRemoveCase {
					pod.Namespace = "foreign"
				}
				if name == changedContainerCase {
					pod.Status.ContainerStatuses[0].ContainerID = "containerd://replacement"
				}
			}
			kube := fake.NewSimpleClientset(node)
			if name == "foreign namespace" || name == daemonCase || name == servingDaemonCase ||
				name == exactRemoveCase || name == foreignRemoveCase ||
				name == changedContainerCase || name == foreignComponentCase {
				_, err := kube.CoreV1().Pods(pod.Namespace).Create(context.Background(), pod, metav1.CreateOptions{})
				if err != nil {
					t.Fatal(err)
				}
			}
			err := validateExecutionNode(context.Background(), kube, m, spec, nil)
			allowed := name == "empty join" || name == "empty rollback" || name == "foreign namespace" ||
				name == daemonCase || name == exactRemoveCase || name == foreignComponentCase
			if (err == nil) != allowed {
				t.Fatalf("unexpected physical safety verdict: %v", err)
			}
		})
	}
}

func TestExecuteOwnerTLSDoesNotForwardCredentialsAcrossRedirect(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("owner-token"), 0600); err != nil {
		t.Fatal(err)
	}
	foreignCalls := 0
	foreign := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { foreignCalls++ }))
	defer foreign.Close()
	owner := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer owner-token" {
			t.Error("explicit owner bearer token missing")
		}
		http.Redirect(w, r, foreign.URL, http.StatusFound)
	}))
	defer owner.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: owner.Certificate().Raw})
	if err := os.WriteFile(ca, certificate, 0600); err != nil {
		t.Fatal(err)
	}
	m := executionManifest{OwnerURL: owner.URL, OwnerCAFile: ca, OwnerTokenFile: token}
	c, err := executionOwnerClient(m)
	if err != nil {
		t.Fatal(err)
	}
	err = readExecutionOwner(context.Background(), c, owner.URL, "/api/v1/scheduler", &json.RawMessage{})
	if err == nil || foreignCalls != 0 {
		t.Fatalf("redirect was followed or accepted: %v, calls=%d", err, foreignCalls)
	}
	for _, missing := range []string{"CA", "token"} {
		without := m
		if missing == "CA" {
			without.OwnerCAFile = ""
		} else {
			without.OwnerTokenFile = ""
		}
		if _, err = executionOwnerClient(without); err == nil {
			t.Fatalf("owner accepted ambient trust/credentials when explicit %s was absent", missing)
		}
	}
	for _, origin := range []string{"http://owner", owner.URL + "/path", owner.URL + "?query=1", "https://user@owner"} {
		m.OwnerURL = origin
		if _, err = executionOwnerClient(m); err == nil {
			t.Fatalf("unsafe owner origin accepted: %s", origin)
		}
	}
}

func TestExecuteCalibrationDoesNotInventAnObservedBootstrap(t *testing.T) {
	spec := servingkube.PrepareSpec{}
	spec.Config.Policy.CompletionOverheadMS = 123
	spec.Config.Policy.EnvelopeSource = "bootstrap-not-observed"
	if err := bindExecutionPolicy(context.Background(), nil, &spec); err != nil ||
		spec.Config.Policy.CompletionOverheadMS != 123 || spec.Instance.CompletionObserved {
		t.Fatalf("configured bootstrap was changed or labelled observed: %+v, %v", spec, err)
	}
	spec.CompletionCalibration = &serving.CompletionCalibration{}
	if err := bindExecutionPolicy(context.Background(), nil, &spec); err == nil {
		t.Fatal("selected but unavailable calibration silently became a bootstrap fallback")
	}
}

func TestExecuteRejectsInlinePreparationBeforeConstructingClients(t *testing.T) {
	m := executionManifest{Namespace: "own", JournalDirectory: "exclusive", SchedulerConfigMap: "scheduler",
		SchedulerConfigKey: "config.json", SourceConfigRV: "10", ProfileID: "profile",
		RoutingHistoryFrom: time.Unix(1, 0),
		Operation: servingkube.Operation{ID: operationPrepare, Kind: operationPrepare,
			Node: serving.NodeReference{Name: "node", UID: "uid"}, Prepare: &servingkube.PrepareSpec{}}}
	if err := runExecution(context.Background(), m, kubeconfig.Options{}, time.Second); err == nil {
		t.Fatal("caller-supplied preparation accepted")
	}
}

func TestExecuteRejectsChangedNativeConfigurationRevision(t *testing.T) {
	kube := fake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Namespace: "own", Name: "scheduler", ResourceVersion: "11",
	}, Data: map[string]string{"config.json": "{}"}})
	m := executionManifest{
		Namespace: "own", SchedulerConfigMap: "scheduler", SchedulerConfigKey: "config.json", SourceConfigRV: "10",
	}
	if _, err := readExecutionConfiguration(context.Background(), kube, m); err == nil ||
		!strings.Contains(err.Error(), "resourceVersion") {
		t.Fatalf("changed native revision was not rejected before decoding: %v", err)
	}
}

func TestExecuteBundleUsesCatalogDeclarationAndValidatesNativePolicy(t *testing.T) {
	for _, cap := range []int{1, 2} {
		profile := &serving.Profile{ID: "profile", Batches: []serving.BatchProfile{{
			Batch: 1, DurationNanos: []float64{2e6, 1e6}, EnergyPerInputJoules: 1,
		}}}
		spec := servingkube.PrepareSpec{
			Instance: serving.SchedulerInstance{Profile: profile},
			Config: native.Config{
				Model: "chill_model", RunnerCommand: []string{"python3", "/opt/chill/runtime/firstfloor/runner.py"},
				Inputs:          []native.InputSpec{{Name: "x", Datatype: "FP32", Shape: []int{1}}},
				RunnerTimeoutMS: 1000, MaxQueueRequests: 4, MaxQueueBytes: 1024, MaxRequestBytes: 1024,
				BatchSizes: []int{1, 2}, MaxBatchSize: 2,
				Policy: native.Policy{DecisionID: "manual", MaxBatchSize: cap, EnvelopeSource: "bootstrap-not-observed"},
			},
		}
		spec.Store.Store = "evidence"
		err := bindExecutionBundle(context.Background(), nil, &spec)
		if cap == 2 {
			if err == nil {
				t.Fatal("initialized but unmeasured batch cap was accepted")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		want := evidencestore.ObjectIdentity{Store: "evidence", Key: "serving/profiles/profile/bundle.json", Size: 90}
		if spec.Bundle != want || spec.Config.ProfilePath != "/profile/bundle.json" ||
			spec.Config.Policy.ProfileID != profile.ID || spec.Config.Policy.Batches != nil {
			t.Fatalf("startup delivery lost catalog identity or embedded mutable samples: %+v", spec)
		}
		if profile.Batches[0].DurationNanos[0] != 2e6 || spec.Instance.CompletionObserved {
			t.Fatal("delivery changed measured samples or invented observed completion")
		}
	}
}

func TestExecutePrepareBudgetAndServingOnlyMembership(t *testing.T) {
	const foreignMembership = "membership with foreign Pods"
	const servingMembership = "membership with serving Pod"
	const foreignComponentCase = "foreign component collision"
	for _, name := range []string{
		"fits", "stale aggregate", "stale readiness", foreignMembership, servingMembership,
		"missing inventory", "missing reservation", "overlap", "CPU overbudget", "memory overbudget",
		"foreign GPU", "serving Pod", "Node unready", "actual memory pressure", foreignComponentCase,
	} {
		t.Run(name, func(t *testing.T) {
			ref := serving.NodeReference{Name: "worker", UID: "node-uid"}
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: ref.Name, UID: "node-uid",
					Labels: map[string]string{servingkube.ServingPoolLabel: "live"}},
				Status: corev1.NodeStatus{
					Allocatable: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("7838032Ki"),
					},
					Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
				},
			}
			foreign := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Namespace: "foreign", Name: "baseline"},
				Spec: corev1.PodSpec{NodeName: ref.Name, Containers: []corev1.Container{{
					Name: "inference", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("2Gi"),
					}},
				}}},
			}
			other := foreign.DeepCopy()
			other.Name = "other"
			other.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("100m")
			other.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = resource.MustParse("256Mi")
			system := foreign.DeepCopy()
			system.Namespace, system.Name = "system", "daemon"
			controller := true
			system.OwnerReferences = []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "DaemonSet", Controller: &controller,
			}}
			system.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("605m")
			system.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = resource.MustParse("210Mi")
			spec := servingkube.PrepareSpec{Instance: serving.SchedulerInstance{
				Node: ref, Pool: "live", Allocation: serving.SchedulerAllocation{
					CPUs: []int{3}, CPUCount: 1, Threads: 1, MemoryBytes: 295698432,
				},
			}}
			m := executionManifest{
				Namespace: "own",
				Operation: servingkube.Operation{Kind: operationPrepare, Node: ref}, ExpectedPool: "live",
			}
			inventory := []serving.SchedulerNode{{
				Node: ref, Pool: "live", Ready: true, CPUs: []int{0, 1, 2, 3},
				Reserved: serving.SchedulerAllocation{CPUs: []int{0, 1}},
			}}
			switch name {
			case "stale aggregate":
				inventory[0].NonServing = []serving.SchedulerAllocation{{CPUCount: 100, MemoryBytes: 1 << 40}}
			case "stale readiness":
				inventory[0].Ready = false
			case foreignMembership:
				m.Operation.Kind = operationMembership
				foreign.Labels = map[string]string{"app.kubernetes.io/component": "serving-instance"}
			case servingMembership:
				m.Operation.Kind = operationMembership
				foreign.Namespace = m.Namespace
				foreign.Labels = map[string]string{"app.kubernetes.io/component": "serving-instance"}
			case "missing inventory":
				inventory = nil
			case "missing reservation":
				inventory[0].Reserved.CPUs = nil
			case "overlap":
				inventory[0].Reserved.CPUs = []int{0, 3}
			case "CPU overbudget":
				foreign.Labels = map[string]string{"app.kubernetes.io/component": "serving-instance"}
				foreign.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("2100m")
			case "memory overbudget":
				node.Status.Allocatable[corev1.ResourceMemory] = resource.MustParse("2Gi")
			case "foreign GPU":
				foreign.Spec.Containers[0].Resources.Requests["nvidia.com/gpu"] = resource.MustParse("1")
			case foreignComponentCase:
				foreign.Labels = map[string]string{"app.kubernetes.io/component": "serving-instance"}
			case "serving Pod":
				foreign.Namespace = m.Namespace
				foreign.Labels = map[string]string{"app.kubernetes.io/component": "serving-instance"}
			case "Node unready":
				node.Status.Conditions[0].Status = corev1.ConditionFalse
			case "actual memory pressure":
				node.Status.Conditions = append(node.Status.Conditions, corev1.NodeCondition{
					Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue,
				})
			}
			kube := fake.NewSimpleClientset(node, foreign, other, system)
			err := validateExecutionNode(context.Background(), kube, m, spec, inventory)
			allowed := name == "fits" || name == "stale aggregate" || name == "stale readiness" ||
				name == foreignMembership || name == foreignComponentCase
			if (err == nil) != allowed {
				t.Fatalf("unexpected fresh preparation/membership admission: %v", err)
			}
		})
	}
}

func TestExecuteReadsDurableAcknowledgementAcrossHistoryPages(t *testing.T) {
	for _, latestStatus := range []string{operationComplete, operationFailed} {
		t.Run(latestStatus, func(t *testing.T) {
			m, config, owner, now := executionOwnerFixture()
			latest := owner.State.Results[0]
			latest.EndedAt, latest.Status = now.Add(-2*time.Minute), latestStatus
			older := latest
			older.EndedAt, older.Status = latest.EndedAt.Add(-time.Second), operationComplete
			unrelated := latest
			unrelated.Operation.SourceConfigRV, unrelated.EndedAt = "another-revision", now
			owner.State.Results = nil
			m.RoutingHistoryFrom = now.Add(-3 * time.Minute)
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				query := r.URL.Query()
				if r.URL.Path != "/api/v1/observations/scheduler" ||
					query.Get("from") != m.RoutingHistoryFrom.UTC().Format(time.RFC3339Nano) ||
					query.Get("to") != now.UTC().Format(time.RFC3339Nano) {
					t.Error("history read changed its explicit fixed interval")
				}
				results, next := []servingkube.OperationResult{older, unrelated}, "2"
				if query.Get("cursor") == "2" {
					results, next = []servingkube.OperationResult{latest}, ""
				}
				events := make([]serving.SchedulerObservation, 0, len(results))
				for _, result := range results {
					raw, err := json.Marshal(result)
					if err != nil {
						t.Error(err)
						return
					}
					events = append(events, serving.SchedulerObservation{
						At: result.EndedAt, Action: &serving.SchedulerActionObservation{
							Kind: manualStaticRouting, Actual: raw, Phase: result.Status,
						},
					})
				}
				if err := json.NewEncoder(w).Encode(struct {
					Events     []serving.SchedulerObservation `json:"events"`
					NextCursor string                         `json:"nextCursor"`
				}{events, next}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			m.OwnerURL = server.URL
			acknowledged, err := readExecutionAcknowledgement(context.Background(), server.Client(), m, now)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 2 || acknowledged.Status != latestStatus || !acknowledged.EndedAt.Equal(latest.EndedAt) {
				t.Fatalf("durable revision selection lost pagination/latest status: %+v, calls=%d", acknowledged, calls)
			}
			err = validateExecutionOwner(m, config, owner, acknowledged, now)
			if (err == nil) != (latestStatus == operationComplete) {
				t.Fatalf("expired runtime results changed durable acknowledgement safety: %v", err)
			}
		})
	}
}
