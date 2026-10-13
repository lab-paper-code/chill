package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"syscall"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"github.com/lab-paper-code/chill/internal/kubeconfig"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/metricobserver/edgemetrics"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	"github.com/lab-paper-code/chill/internal/serving"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type commandManifest struct {
	Measurement        servingkube.MeasurementRequest           `json:"measurement"`
	Campaigns          []types.NamespacedName                   `json:"campaigns"`
	StoreEndpoint      string                                   `json:"storeEndpoint"`
	StoreBucket        string                                   `json:"storeBucket"`
	StoreRegion        string                                   `json:"storeRegion"`
	MetricSources      profilerkube.MetricSourceResolverOptions `json:"metricSources"`
	Namespace          string                                   `json:"namespace"`
	ActuationNamespace string                                   `json:"actuationNamespace"`
	JournalDirectory   string                                   `json:"journalDirectory"`
	IsolatedPool       string                                   `json:"isolatedPool"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context) error {
	var file string
	var apiServer, token, ca string
	var timeout time.Duration
	flag.StringVar(&file, "measurement-config", "", "bounded JSON atomic operation measurement manifest")
	flag.StringVar(&apiServer, "kube-api-server", "", "Kubernetes API endpoint")
	flag.StringVar(&token, "kube-token-file", "", "Kubernetes token path")
	flag.StringVar(&ca, "kube-ca-file", "", "Kubernetes CA path")
	flag.DurationVar(&timeout, "timeout", 10*time.Minute, "entire real operation observation bound")
	flag.Parse()
	if file == "" || timeout <= 0 {
		return errors.New("measurement-config and positive timeout required")
	}
	manifest, err := readManifest(file)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return runMeasurement(ctx, manifest, kubeconfig.Options{APIServer: apiServer, TokenFile: token, CAFile: ca}, timeout)
}

func runMeasurement(ctx context.Context, manifest commandManifest,
	kubeOptions kubeconfig.Options, timeout time.Duration,
) (retErr error) {
	config, err := kubeconfig.BuildConfig(kubeOptions)
	if err != nil {
		return err
	}
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	scheme := runtime.NewScheme()
	if err = clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err = api.AddToScheme(scheme); err != nil {
		return err
	}
	store, err := servingkube.NewStore(config, scheme)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(manifest.IsolatedPool, "measurement-") ||
		manifest.Namespace == "" || manifest.JournalDirectory == "" {
		return errors.New("explicit measurement-* isolated pool, restricted namespace " +
			"and exclusive observation directory required")
	}
	direct, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	if err = validateMeasurementNode(ctx, kube, manifest); err != nil {
		return err
	}
	objects, err := stores3.New(stores3.Config{
		LogicalStore: manifest.Measurement.Location.Store, Endpoint: manifest.StoreEndpoint,
		Bucket: manifest.StoreBucket, Region: manifest.StoreRegion,
		AccessKeyID: os.Getenv("AWS_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		SessionToken: os.Getenv("AWS_SESSION_TOKEN"),
	})
	if err != nil {
		return err
	}
	ownership, err := serving.AcquireObservationOwnership(manifest.JournalDirectory)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, ownership.Close()) }()
	journal, err := serving.NewJournal(serving.JournalConfig{
		Directory: manifest.JournalDirectory, Store: manifest.Measurement.Location.Store,
		Window: time.Minute, Writer: objects,
	})
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, journal.Close(context.WithoutCancel(ctx))) }()
	execution, err := serving.NewRuntime(ctx, serving.RuntimeConfig{QueueLimit: 1024, Journal: journal})
	if err != nil {
		return err
	}
	if len(execution.SchedulerOutstanding(time.Now())) != 0 {
		return errors.New("measurement observation directory contains accepted outstanding traffic")
	}
	native := &sdk.Client{HTTPClient: &http.Client{Timeout: timeout}}
	observer, err := servingkube.NewObserver(store, kube, execution, native,
		servingkube.ObserverConfig{
			Namespace: manifest.Namespace, ActuationNamespace: manifest.ActuationNamespace,
		})
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, observer.Close()) }()
	catalog, err := servingkube.NewCatalog(store, objects, 64<<20, 0.95)
	if err != nil {
		return err
	}
	view, err := catalog.Refresh(ctx, manifest.Campaigns)
	if err != nil {
		return err
	}
	observer.SetProfiles(view.Profiles)
	if err = bindMeasurementProfile(manifest.Measurement.Operation.Prepare, view.Profiles); err != nil {
		return err
	}
	models := map[string]bool{}
	for _, p := range view.Profiles {
		if p.Condition.NodeUID == manifest.Measurement.Operation.Node.UID {
			models[p.Condition.LogicalModel] = true
		}
	}
	readIsolation := func(checkCtx context.Context, expectedPool string) (servingkube.MeasurementIsolation, error) {
		return readMeasurementIsolation(checkCtx, kube, direct,
			manifest.Measurement.Operation.Node, expectedPool, models)
	}
	if _, err = readIsolation(ctx, manifest.IsolatedPool); err != nil {
		return err
	}
	isolationCheck := func(checkCtx context.Context) (servingkube.MeasurementIsolation, error) {
		pool := manifest.IsolatedPool
		if manifest.Measurement.Operation.Kind == "membership" {
			pool = manifest.Measurement.Operation.Pool
		}
		return readIsolation(checkCtx, pool)
	}
	if err = observer.Refresh(ctx); err != nil {
		return err
	}
	operations, err := servingkube.NewOperations(store, kube, execution, observer, native,
		servingkube.OperationConfig{
			Namespace: manifest.Namespace, ActuationNamespace: manifest.ActuationNamespace,
			PollInterval: 100 * time.Millisecond,
		})
	if err != nil {
		return err
	}
	source, err := measurementPowerSource(ctx, direct, manifest)
	if err != nil {
		return err
	}
	record, receipt, measureErr := operations.Measure(ctx, manifest.Measurement, source, objects, isolationCheck)
	if err = json.NewEncoder(os.Stdout).Encode(struct {
		Record  servingkube.OperationMeasurement `json:"record"`
		Receipt any                              `json:"receipt"`
	}{record, receipt}); err != nil {
		return errors.Join(measureErr, err)
	}
	if record.Verdict != "Accepted" {
		measureErr = errors.Join(measureErr, fmt.Errorf("measurement %s: %s", record.Verdict, record.Reason))
	}
	return measureErr
}

func readManifest(path string) (commandManifest, error) {
	var manifest commandManifest
	stream, err := os.Open(path)
	if err != nil {
		return manifest, err
	}
	raw, err := io.ReadAll(io.LimitReader(stream, 4<<20+1))
	err = errors.Join(err, stream.Close())
	if err != nil {
		return manifest, err
	}
	if len(raw) > 4<<20 {
		return manifest, errors.New("measurement manifest exceeds bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&manifest); err != nil {
		return manifest, err
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return manifest, errors.New("measurement manifest has trailing JSON")
	}
	return manifest, nil
}

func validateMeasurementNode(ctx context.Context, kube kubernetes.Interface, manifest commandManifest) error {
	physical, err := kube.CoreV1().Nodes().Get(ctx, manifest.Measurement.Operation.Node.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if string(physical.UID) != manifest.Measurement.Operation.Node.UID ||
		physical.Labels[servingkube.ServingPoolLabel] != manifest.IsolatedPool {
		return errors.New("actual physical Node must belong to explicitly isolated measurement pool")
	}
	if prepare := manifest.Measurement.Operation.Prepare; prepare != nil &&
		prepare.Instance.Pool != manifest.IsolatedPool {
		return errors.New("measurement preparation must remain in isolated pool")
	}
	if manifest.Measurement.Operation.Kind == "membership" &&
		!strings.HasPrefix(manifest.Measurement.Operation.Pool, "measurement-") {
		return errors.New("measurement membership target must remain measurement-* isolated pool")
	}
	return nil
}

func bindMeasurementProfile(prepare *servingkube.PrepareSpec, profiles []serving.Profile) error {
	if prepare == nil {
		return nil
	}
	for i := range profiles {
		p := &profiles[i]
		if prepare.Instance.Profile != nil && p.ID == prepare.Instance.Profile.ID {
			prepare.Instance.Profile = p
			return nil
		}
	}
	return errors.New("operation preparation has no catalog-verified selected profile")
}

func readMeasurementIsolation(ctx context.Context, kube kubernetes.Interface, direct client.Client,
	expectedNode serving.NodeReference, expectedPool string, models map[string]bool,
) (servingkube.MeasurementIsolation, error) {
	witness := servingkube.MeasurementIsolation{Workloads: []serving.WorkloadReference{}}
	node, err := kube.CoreV1().Nodes().Get(ctx, expectedNode.Name, metav1.GetOptions{})
	if err != nil {
		return witness, err
	}
	witness.Node = serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	witness.Pool = node.Labels[servingkube.ServingPoolLabel]
	var workloads api.WorkloadList
	if err = direct.List(ctx, &workloads); err != nil {
		return witness, err
	}
	for _, w := range workloads.Items {
		if models[w.Spec.ModelRef.Name] ||
			(w.Status.AppliedDefinition != nil && models[w.Status.AppliedDefinition.ModelRef.Name]) {
			witness.Workloads = append(witness.Workloads, serving.WorkloadReference{Name: w.Name, UID: string(w.UID)})
		}
	}
	witness.CheckedAt = time.Now().UTC()
	if witness.Node != expectedNode || witness.Pool != expectedPool || len(witness.Workloads) != 0 {
		return witness, errors.New("actual physical Node/pool or Workload ownership violates measurement isolation")
	}
	return witness, nil
}

func measurementPowerSource(ctx context.Context, direct client.Client,
	manifest commandManifest,
) (metricobserver.Source, error) {
	resolver, err := profilerkube.NewKubernetesMetricSourceResolver(direct, manifest.MetricSources)
	if err != nil {
		return nil, err
	}
	resolved, err := resolver.ResolveMetricSource(ctx, manifest.Measurement.Operation.Node.Name)
	if err != nil {
		return nil, err
	}
	identity := metricobserver.SourceIdentity{
		Kind: resolved.Kind, NodeName: manifest.Measurement.Operation.Node.Name, Endpoint: resolved.Endpoint,
		Metric: resolved.PowerMetric, MetricLabels: resolved.MetricLabels,
	}
	// The declaration must equal the current Node-resolved source; do not accept
	// an arbitrary manifest URL as evidence that the meter is node-exclusive.
	if !reflect.DeepEqual(identity, manifest.Measurement.Meter.Source) {
		return nil, errors.New("declared meter differs from actual Node-resolved metric source")
	}
	return edgemetrics.NewPowerSeries(identity, &http.Client{})
}
