package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	operatorprofiler "github.com/lab-paper-code/chill/internal/operator/profiler"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign/controller"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

func TestArgumentErrorsNeverEchoCredentials(t *testing.T) {
	for _, args := range [][]string{{}, {"--endpoint", "secret-credential"}, {"--uid=secret-credential"}} {
		var out bytes.Buffer
		if run(context.Background(), args, &out) == 0 {
			t.Fatal("invalid invocation succeeded")
		}
		var proof campaign.PrejobFailureInspection
		if json.Unmarshal(out.Bytes(), &proof) != nil || proof.Version != 1 || proof.Eligible || strings.Contains(out.String(), "secret-credential") {
			t.Fatalf("unsafe output: %s", out.Bytes())
		}
	}
}

// TestCompiledCLIReadOnlyLocalAPI runs the already compiled command against
// real HTTP Kubernetes discovery/GET and an S3 NoSuchBucket response. Neither
// server accepts mutations. The authority package separately exercises eligible
// 2027/1792/1970 schedules with real Preparer and retained evidence decoding.
// Run with CHILL_PREJOB_CHECK_BIN set after building the command once.
func TestCompiledCLIReadOnlyLocalAPI(t *testing.T) {
	binary := os.Getenv("CHILL_PREJOB_CHECK_BIN")
	if binary == "" {
		t.Skip("set CHILL_PREJOB_CHECK_BIN to the compiled prejob-retirement-check")
	}
	listener, err := net.Listen("tcp", "127.0.0.99:9000")
	if err != nil {
		t.Fatal(err)
	}
	var reads, writes atomic.Int64
	store := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
			http.Error(w, "mutation forbidden", http.StatusMethodNotAllowed)
			return
		}
		reads.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<Error><Code>NoSuchBucket</Code><Message>private-backend-secret</Message></Error>`))
	}))
	store.Listener = listener
	store.Start()
	defer store.Close()
	objects := localAPIObjects(t)
	var apiReads atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
			http.Error(w, "mutation forbidden", http.StatusMethodNotAllowed)
			return
		}
		apiReads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if discovery, ok := localDiscovery(r.URL.Path); ok {
			_ = json.NewEncoder(w).Encode(discovery)
			return
		}
		object, ok := objects[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: 404})
			return
		}
		_ = json.NewEncoder(w).Encode(object)
	}))
	defer api.Close()
	config := filepath.Join(t.TempDir(), "config")
	payload := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: local\n  cluster:\n    server: %s\ncontexts:\n- name: local\n  context:\n    cluster: local\n    user: local\ncurrent-context: local\nusers:\n- name: local\n  user: {}\n", api.URL)
	if os.WriteFile(config, []byte(payload), 0o600) != nil {
		t.Fatal("write local kubeconfig")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "--kubeconfig", config, "--context", "local", "--namespace", "chill-system", "--system-name", "chill", "--name", "prejob", "--uid", "request-uid")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if command.Run() == nil {
		t.Fatal("NoSuchBucket accepted")
	}
	var proof campaign.PrejobFailureInspection
	if err := json.Unmarshal(stdout.Bytes(), &proof); err != nil {
		t.Fatalf("CLI JSON: %v, stderr=%s, stdout=%s", err, stderr.Bytes(), stdout.Bytes())
	}
	if proof.Eligible || proof.Reason != "RecordedPlanReadFailed" || reads.Load() == 0 || apiReads.Load() < 5 || writes.Load() != 0 {
		t.Fatalf("not actual API/S3 refusal: %+v API=%d S3=%d writes=%d stderr=%s", proof, apiReads.Load(), reads.Load(), writes.Load(), stderr.Bytes())
	}
	if strings.Contains(stdout.String()+stderr.String(), "private-backend-secret") || strings.Contains(stdout.String()+stderr.String(), "credential-value") {
		t.Fatal("credential/backend detail leaked")
	}
}

func localAPIObjects(t *testing.T) map[string]any {
	t.Helper()
	settings := operatorprofiler.Config{Replicas: 1, Image: "profiler:test", ImagePullPolicy: corev1.PullIfNotPresent,
		ServiceAccountName: "profiler", MetricsBindAddress: "0", HealthProbePort: 8081,
		FailedJobRetentionSeconds: 86400, MeasurementMemoryFraction: .8,
		FactSource:         operatorprofiler.FactSourceConfig{ConfigMapName: "facts", ExecutionFactsKey: "execution", MetricSourcesKey: "metric", WorkloadObservationKey: "workload", ExecutionPoliciesKey: "policy", ActuationPoliciesKey: "actuation"},
		WorkloadObserver:   operatorprofiler.WorkloadObserverConfig{ServiceAccountName: "workload"},
		TransitionObserver: operatorprofiler.TransitionObserverConfig{ObserverNodeName: "observer", ServiceAccountName: "transition", ActiveDeadlineOverheadSeconds: 180, MaximumObjectBytes: 64 << 20}}
	raw, err := yaml.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	system := &edge.ChillSystem{TypeMeta: metav1.TypeMeta{APIVersion: edge.GroupVersion.String(), Kind: "ChillSystem"}, ObjectMeta: metav1.ObjectMeta{Name: "chill", UID: "system-uid", Generation: 1}}
	system.Spec.ManagementNamespace = "chill-system"
	system.Spec.Profiler = edge.ChillProfilerSpec{Enabled: true, ConfigRef: corev1.LocalObjectReference{Name: "profiler-config"}}
	system.Spec.EvidenceStore = edge.ChillEvidenceStoreSpec{Enabled: true, ConfigRef: corev1.LocalObjectReference{Name: "store-config"}}
	system.Status.ObservedGeneration = 1
	system.Status.Conditions = []metav1.Condition{{Type: edge.ChillSystemConditionEvidenceStoreAvailable, Status: metav1.ConditionTrue, ObservedGeneration: 1}}
	owner := []metav1.OwnerReference{*metav1.NewControllerRef(system, edge.GroupVersion.WithKind("ChillSystem"))}
	configMap := func(name string, data map[string]string) *corev1.ConfigMap {
		return &corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: name, ResourceVersion: "10"}, Data: data}
	}
	bootstrap := &batchv1.Job{TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"}, ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: component.EvidenceStoreBootstrapJobName("chill"), OwnerReferences: owner, Annotations: map[string]string{component.EvidenceStoreConfigResourceVersionAnnotation: "10"}}, Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}}}
	service := &corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: component.EvidenceStoreServiceName("chill"), OwnerReferences: owner}, Spec: corev1.ServiceSpec{ClusterIP: "127.0.0.99", Ports: []corev1.ServicePort{{Port: 9000, Protocol: corev1.ProtocolTCP}}}}
	secret := &corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: component.EvidenceStoreCredentialsSecretName("chill")}, Data: map[string][]byte{component.EvidenceStoreAccessKeyKey: []byte("credential-value"), component.EvidenceStoreSecretKeyKey: []byte("credential-value")}}
	parent := &edge.BatchEnergyOptimization{TypeMeta: metav1.TypeMeta{APIVersion: edge.GroupVersion.String(), Kind: "BatchEnergyOptimization"}, ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "prejob", UID: "request-uid", ResourceVersion: "42", Generation: 1, Annotations: map[string]string{profilingrun.AdmissionAnnotation: profilingrun.GrantedAdmission(time.Unix(0, 0))}}}
	parent.Spec.TrialsPerBatch = 3
	parent.Status = edge.BatchEnergyOptimizationStatus{ObservedGeneration: 1, Phase: "Measuring", CompletedTrials: 3, Conditions: []metav1.Condition{{Type: "MeasurementComplete", Status: metav1.ConditionUnknown, Reason: "TrialInFlight", ObservedGeneration: 1}}}
	return map[string]any{
		"/apis/edge.dacs.io/v1alpha1/chillsystems/chill":                                      system,
		"/api/v1/namespaces/chill-system/configmaps/profiler-config":                          configMap("profiler-config", map[string]string{"config.yaml": string(raw)}),
		"/api/v1/namespaces/chill-system/configmaps/store-config":                             configMap("store-config", map[string]string{"config.yaml": "bucket: profiling\nregion: us-east-1\n"}),
		"/apis/batch/v1/namespaces/chill-system/jobs/" + bootstrap.Name:                       bootstrap,
		"/api/v1/namespaces/chill-system/services/" + service.Name:                            service,
		"/api/v1/namespaces/chill-system/secrets/" + secret.Name:                              secret,
		"/apis/edge.dacs.io/v1alpha1/namespaces/chill-system/batchenergyoptimizations/prejob": parent,
	}
}

func localDiscovery(path string) (any, bool) {
	type resource struct {
		name, kind string
		namespaced bool
	}
	groups := []metav1.APIGroup{
		{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "APIGroup"}, Name: "edge.dacs.io", Versions: []metav1.GroupVersionForDiscovery{{GroupVersion: "edge.dacs.io/v1alpha1", Version: "v1alpha1"}}, PreferredVersion: metav1.GroupVersionForDiscovery{GroupVersion: "edge.dacs.io/v1alpha1", Version: "v1alpha1"}},
		{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "APIGroup"}, Name: "batch", Versions: []metav1.GroupVersionForDiscovery{{GroupVersion: "batch/v1", Version: "v1"}}, PreferredVersion: metav1.GroupVersionForDiscovery{GroupVersion: "batch/v1", Version: "v1"}},
	}
	if path == "/api" {
		return metav1.APIVersions{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "APIVersions"}, Versions: []string{"v1"}}, true
	}
	if path == "/apis" {
		return metav1.APIGroupList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "APIGroupList"}, Groups: groups}, true
	}
	if path == "/apis/edge.dacs.io" {
		return groups[0], true
	}
	if path == "/apis/batch" {
		return groups[1], true
	}
	var gv schema.GroupVersion
	var resources []resource
	switch path {
	case "/api/v1":
		gv = corev1.SchemeGroupVersion
		resources = []resource{{"configmaps", "ConfigMap", true}, {"secrets", "Secret", true}, {"services", "Service", true}, {"nodes", "Node", false}, {"pods", "Pod", true}}
	case "/apis/edge.dacs.io/v1alpha1":
		gv = edge.GroupVersion
		resources = []resource{{"chillsystems", "ChillSystem", false}, {"batchenergyoptimizations", "BatchEnergyOptimization", true}, {"profilingruns", "ProfilingRun", true}}
	case "/apis/batch/v1":
		gv = batchv1.SchemeGroupVersion
		resources = []resource{{"jobs", "Job", true}}
	default:
		return nil, false
	}
	list := metav1.APIResourceList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "APIResourceList"}, GroupVersion: gv.String()}
	for _, r := range resources {
		list.APIResources = append(list.APIResources, metav1.APIResource{Name: r.name, Kind: r.kind, Namespaced: r.namespaced, Verbs: metav1.Verbs{"get", "list"}})
	}
	return list, true
}
