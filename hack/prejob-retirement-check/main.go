// Command prejob-retirement-check reads the current configured authorities and
// emits a bounded receipt. It never reconciles or changes Kubernetes or S3.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	operatorprofiler "github.com/lab-paper-code/chill/internal/operator/profiler"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign/controller"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiling/profiler/controller"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	evidencekube "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence/kube"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Stdout))
}

func run(ctx context.Context, args []string, out io.Writer) int {
	flags := flag.NewFlagSet("prejob-retirement-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var kubeconfig, kubeContext, namespace, system, name, uid string
	flags.StringVar(&kubeconfig, "kubeconfig", "", "explicit kubeconfig path")
	flags.StringVar(&kubeContext, "context", "", "explicit context")
	flags.StringVar(&namespace, "namespace", "", "request namespace")
	flags.StringVar(&system, "system-name", "", "configured ChillSystem")
	flags.StringVar(&name, "name", "", "request name")
	flags.StringVar(&uid, "uid", "", "exact request UID")
	proof := campaign.PrejobFailureInspection{Version: 1, Reason: "InspectionFailed", CheckedAt: time.Now().UTC()}
	finish := func(reason string) int {
		if reason != "" {
			proof.Eligible, proof.Reason = false, reason
		}
		if json.NewEncoder(out).Encode(proof) != nil {
			return 1
		}
		if proof.Eligible {
			return 0
		}
		return 1
	}
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return finish("InvalidArguments")
	}
	for _, value := range []string{kubeconfig, kubeContext, namespace, system, name, uid} {
		if value == "" || strings.TrimSpace(value) != value {
			return finish("InvalidArguments")
		}
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}, &clientcmd.ConfigOverrides{CurrentContext: kubeContext},
	).ClientConfig()
	if err != nil {
		return finish("KubeconfigReadFailed")
	}
	scheme := runtime.NewScheme()
	if clientgoscheme.AddToScheme(scheme) != nil || edge.AddToScheme(scheme) != nil {
		return finish("SchemeFailed")
	}
	reader, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return finish("KubernetesReaderFailed")
	}
	proof, err = inspectConfigured(ctx, reader, namespace, system, name, uid)
	if err != nil {
		return finish(proof.Reason)
	}
	return finish("")
}

func configuredObjects(binding storekube.Binding, credentials storekube.Credentials) (*stores3.Store, error) {
	return stores3.New(stores3.Config{LogicalStore: binding.Store, Bucket: binding.Bucket, Endpoint: binding.Endpoint, Region: binding.Region,
		AccessKeyID: credentials.AccessKeyID, SecretAccessKey: credentials.SecretAccessKey, TempDir: component.ProfilerEvidenceStagingPath})
}

func inspectConfigured(ctx context.Context, reader client.Reader, namespace, system, name, uid string) (campaign.PrejobFailureInspection, error) {
	proof := campaign.PrejobFailureInspection{Version: 1, CheckedAt: time.Now().UTC()}
	finish := func(reason string) (campaign.PrejobFailureInspection, error) {
		proof.Eligible, proof.Reason = false, reason
		return proof, errors.New(reason)
	}
	instance := &edge.ChillSystem{}
	if reader.Get(ctx, types.NamespacedName{Name: system}, instance) != nil || !instance.Spec.Profiler.Enabled || instance.Spec.Profiler.ConfigRef.Name == "" || (instance.Spec.ManagementNamespace != "" && instance.Spec.ManagementNamespace != namespace) {
		return finish("ProfilerBindingReadFailed")
	}
	cm := &corev1.ConfigMap{}
	if reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: instance.Spec.Profiler.ConfigRef.Name}, cm) != nil {
		return finish("ProfilerConfigReadFailed")
	}
	var settings operatorprofiler.Config
	if yaml.Unmarshal([]byte(cm.Data[operatorprofiler.DefaultConfigKey]), &settings) != nil || settings.Validate() != nil {
		return finish("ProfilerConfigInvalid")
	}
	facts := settings.FactSource
	candidates, err := profilerkube.NewKubernetesCandidateResolver(reader, profilerkube.CandidateResolverOptions{Namespace: namespace, ConfigMapName: facts.ConfigMapName, ConfigMapKey: facts.ExecutionFactsKey})
	if err != nil {
		return finish("CandidateResolverFailed")
	}
	nodes, err := profilerkube.NewKubernetesNodeResolver(reader)
	if err != nil {
		return finish("NodeResolverFailed")
	}
	metrics, err := profilerkube.NewKubernetesMetricSourceResolver(reader, profilerkube.MetricSourceResolverOptions{Namespace: namespace, ConfigMapName: facts.ConfigMapName, ConfigMapKey: facts.MetricSourcesKey})
	if err != nil {
		return finish("MetricResolverFailed")
	}
	workloads, err := profilerkube.NewKubernetesWorkloadObservationResolver(reader, profilerkube.WorkloadObservationResolverOptions{Namespace: namespace, ConfigMapName: facts.ConfigMapName, ConfigMapKey: facts.WorkloadObservationKey})
	if err != nil {
		return finish("WorkloadResolverFailed")
	}
	actuation, err := profilerkube.NewKubernetesActuationPolicyResolver(reader, profilerkube.ActuationPolicyResolverOptions{Namespace: namespace, ConfigMapName: facts.ConfigMapName, ConfigMapKey: facts.ActuationPoliciesKey})
	if err != nil {
		return finish("ActuationResolverFailed")
	}
	execution, err := profilerkube.NewKubernetesExecutionPolicyResolver(reader, profilerkube.ExecutionPolicyResolverOptions{Namespace: namespace, ConfigMapName: facts.ConfigMapName, ConfigMapKey: facts.ExecutionPoliciesKey, MeasurementMemoryFraction: settings.MeasurementMemoryFraction})
	if err != nil {
		return finish("ExecutionResolverFailed")
	}
	preparer, err := profiler.NewPreparer(candidates, nodes, metrics, workloads, actuation, execution)
	if err != nil {
		return finish("PreparerFailed")
	}
	stores, err := profilerkube.NewKubernetesEvidenceStoreResolver(reader, profilerkube.EvidenceStoreResolverOptions{SystemName: system, Namespace: namespace})
	if err != nil {
		return finish("StoreResolverFailed")
	}
	binding, err := stores.Resolve(ctx)
	if err != nil || binding.Store != component.EvidenceStoreServiceName(system) {
		return finish("StoreBindingReadFailed")
	}
	credentials, err := storekube.LoadCredentials(ctx, reader, namespace, binding.CredentialsSecretName)
	if err != nil {
		return finish("StoreCredentialsReadFailed")
	}
	objects, err := configuredObjects(binding, credentials)
	if err != nil {
		return finish("StoreReaderFailed")
	}
	plans, err := campaign.NewCurvePlanSource(reader, candidates, preparer, objects, campaign.CurvePlanSourceOptions{Store: binding.Store, MaxObjectBytes: 64 << 20})
	if err != nil {
		return finish("PlanReaderFailed")
	}
	resolver, err := profilercontroller.NewRunConfigResolver(preparer, execution, reader, plans.ReadRecorded)
	if err != nil {
		return finish("ChildResolverFailed")
	}
	retained, err := evidencekube.NewRetainedLoader(reader, func(binding storekube.Binding, credentials storekube.Credentials) (evidencestore.LocationReader, error) {
		return configuredObjects(binding, credentials)
	})
	if err != nil {
		return finish("RetainedReaderFailed")
	}
	proof, err = campaign.InspectPrejobFailure(ctx, reader, objects, plans, resolver,
		func(ctx context.Context, run *edge.ProfilingRun, identity evidencestore.ObjectIdentity) (profilerevidence.VerifiedIdleEvidence, error) {
			verified, err := retained.LoadIdle(ctx, run, identity)
			return verified.ForDerivation(), err
		}, binding.Store, types.NamespacedName{Namespace: namespace, Name: name}, types.UID(uid))
	return proof, err
}
