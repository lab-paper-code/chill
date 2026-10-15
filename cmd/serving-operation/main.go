package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"syscall"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/kubeconfig"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/metricobserver/edgemetrics"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/entrypoint"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	operationPrepare    = "prepare"
	operationRemove     = "remove"
	operationMembership = "membership"
	operationComplete   = "complete"
	operationFailed     = "failed"
	manualStaticRouting = "manual-static-routing"
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
	var file, executeFile string
	var apiServer, token, ca string
	var timeout time.Duration
	flag.StringVar(&file, "measurement-config", "", "bounded JSON atomic operation measurement manifest")
	flag.StringVar(&executeFile, "execute-config", "",
		"bounded JSON manual prepare/remove/membership manifest (not a measurement)")
	flag.StringVar(&apiServer, "kube-api-server", "", "Kubernetes API endpoint")
	flag.StringVar(&token, "kube-token-file", "", "Kubernetes token path")
	flag.StringVar(&ca, "kube-ca-file", "", "Kubernetes CA path")
	flag.DurationVar(&timeout, "timeout", 10*time.Minute, "entire real operation observation bound")
	flag.Parse()
	if (file == "") == (executeFile == "") || timeout <= 0 {
		return errors.New("exactly one of measurement-config or execute-config and positive timeout required")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	options := kubeconfig.Options{APIServer: apiServer, TokenFile: token, CAFile: ca}
	if executeFile != "" {
		var manifest executionManifest
		if err := readJSONManifest(executeFile, &manifest); err != nil {
			return err
		}
		return runExecution(ctx, manifest, options, timeout)
	}
	manifest, err := readManifest(file)
	if err != nil {
		return err
	}
	return runMeasurement(ctx, manifest, options, timeout)
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
	initialPool, err := validateMeasurementNode(ctx, kube, manifest)
	if err != nil {
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
	if _, err = readIsolation(ctx, initialPool); err != nil {
		return err
	}
	isolationCheck := func(checkCtx context.Context) (servingkube.MeasurementIsolation, error) {
		pool := manifest.IsolatedPool
		if manifest.Measurement.Operation.Kind == operationMembership {
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
	err := readJSONManifest(path, &manifest)
	return manifest, err
}

func readJSONManifest(path string, manifest any) error {
	stream, err := os.Open(path)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(stream, 4<<20+1))
	err = errors.Join(err, stream.Close())
	if err != nil {
		return err
	}
	if len(raw) > 4<<20 {
		return errors.New("operation manifest exceeds bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(manifest); err != nil {
		return err
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("operation manifest has trailing JSON")
	}
	return nil
}

func validateMeasurementNode(ctx context.Context, kube kubernetes.Interface, manifest commandManifest) (string, error) {
	physical, err := kube.CoreV1().Nodes().Get(ctx, manifest.Measurement.Operation.Node.Name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	pool := physical.Labels[servingkube.ServingPoolLabel]
	if string(physical.UID) != manifest.Measurement.Operation.Node.UID {
		return "", errors.New("actual physical Node identity differs from measurement")
	}
	if manifest.Measurement.Operation.Kind == operationMembership {
		target := manifest.Measurement.Operation.Pool
		if pool == target || (pool != "" && pool != manifest.IsolatedPool) ||
			(target != "" && target != manifest.IsolatedPool) {
			return "", errors.New("measurement membership must change between detached and explicitly isolated pool states")
		}
	} else if pool != manifest.IsolatedPool {
		return "", errors.New("actual physical Node must belong to explicitly isolated measurement pool")
	}
	if prepare := manifest.Measurement.Operation.Prepare; prepare != nil &&
		prepare.Instance.Pool != manifest.IsolatedPool {
		return "", errors.New("measurement preparation must remain in isolated pool")
	}
	return pool, nil
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

// Manual execution is separate from zero-inference operation-cost measurement.
// Routing remains the live owner's complete-map staticRouting configuration.
type executionManifest struct {
	Operation          servingkube.Operation `json:"operation"`
	ProfileID          string                `json:"profileID"`
	Namespace          string                `json:"namespace"`
	ActuationNamespace string                `json:"actuationNamespace"`
	JournalDirectory   string                `json:"journalDirectory"`
	OwnerURL           string                `json:"ownerURL"`
	OwnerCAFile        string                `json:"ownerCAFile"`
	OwnerTokenFile     string                `json:"ownerTokenFile"`
	SchedulerConfigMap string                `json:"schedulerConfigMap"`
	SchedulerConfigKey string                `json:"schedulerConfigKey"`
	SourceConfigRV     string                `json:"sourceConfigRV"`
	ExpectedPool       string                `json:"expectedPool"`
	RoutingHistoryFrom time.Time             `json:"routingHistoryFrom"`
}

type executionConfiguration struct {
	Settings         serving.SchedulerSettings          `json:"settings"`
	ProfileCampaigns []entrypoint.ProfileCampaign       `json:"profileCampaigns"`
	Preparations     map[string]servingkube.PrepareSpec `json:"preparations"`
	StaticRouting    map[string]map[string]int          `json:"staticRouting"`
}

type executionOwnerView struct {
	Settings serving.SchedulerSettings            `json:"settings"`
	State    servingkube.SchedulerControllerState `json:"state"`
}

func executionOwnerClient(m executionManifest) (*http.Client, error) {
	u, err := url.Parse(m.OwnerURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("ownerURL must be one verified HTTPS origin without credentials, path, query or fragment")
	}
	if m.OwnerTokenFile == "" || m.OwnerCAFile == "" {
		return nil, errors.New("explicit ownerCAFile and ownerTokenFile required; " +
			"Kubernetes credentials are not forwarded")
	}
	c, err := rest.HTTPClientFor(&rest.Config{
		Host: m.OwnerURL, BearerTokenFile: m.OwnerTokenFile,
		TLSClientConfig: rest.TLSClientConfig{CAFile: m.OwnerCAFile},
	})
	if err != nil {
		return nil, err
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c, nil
}

func readExecutionOwner(ctx context.Context, c *http.Client, origin, path string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(origin, "/")+path, nil)
	if err != nil {
		return err
	}
	response, err := c.Do(request)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 64<<20+1))
	err = errors.Join(err, response.Body.Close())
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK || len(raw) > 64<<20 {
		return fmt.Errorf("bounded owner read %s failed (HTTP %d)", path, response.StatusCode)
	}
	return json.Unmarshal(raw, target)
}

func readExecutionConfiguration(
	ctx context.Context, kube kubernetes.Interface, m executionManifest,
) (executionConfiguration, error) {
	var config executionConfiguration
	cm, err := kube.CoreV1().ConfigMaps(m.Namespace).Get(ctx, m.SchedulerConfigMap, metav1.GetOptions{})
	if err != nil {
		return config, err
	}
	if cm.ResourceVersion != m.SourceConfigRV {
		return config, errors.New("scheduler ConfigMap native resourceVersion changed; query the owner again")
	}
	raw := cm.Data[m.SchedulerConfigKey]
	if len(raw) > 4<<20 {
		return config, errors.New("scheduler configuration exceeds bound")
	}
	if err = json.Unmarshal([]byte(raw), &config); err != nil {
		return config, err
	}
	if err = config.Settings.Validate(); err != nil {
		return config, err
	}
	if config.Settings.Controls.Enabled || config.StaticRouting == nil {
		return config, errors.New("manual execution requires desired controls OFF and explicit complete staticRouting")
	}
	return config, nil
}

func validateExecutionOwner(
	m executionManifest, config executionConfiguration, owner executionOwnerView,
	acknowledged *servingkube.OperationResult, checkedAt time.Time,
) error {
	if config.Settings.Controls.Enabled || owner.Settings.Controls.Enabled {
		return errors.New("desired and live owner controls must be OFF")
	}
	for _, action := range owner.State.Actions {
		if action.Status == "running" || action.Status == "cancel" {
			return errors.New("live owner has an unfinished control action")
		}
	}
	if acknowledged == nil || acknowledged.Operation.Kind != manualStaticRouting ||
		acknowledged.Operation.SourceConfigRV != m.SourceConfigRV ||
		acknowledged.Status != operationComplete || acknowledged.EndedAt.IsZero() ||
		!reflect.DeepEqual(acknowledged.Operation.StaticRouting, config.StaticRouting) {
		return errors.New("current complete staticRouting revision lacks a successful owner acknowledgement")
	}
	snapshot := owner.State.Snapshot
	if !snapshot.At.After(acknowledged.EndedAt) || snapshot.At.Before(checkedAt.Add(-owner.Settings.ControlInterval)) ||
		snapshot.At.After(checkedAt) {
		return errors.New("owner snapshot must be fresh and later than current staticRouting completion")
	}
	// Use the existing complete-map validation, including current Workload UIDs.
	target, err := serving.StaticRoutingTarget(snapshot, config.StaticRouting, "manual-execution-preflight")
	if err != nil {
		return err
	}
	equalRouting := func(a, b map[string]map[string]int32) bool {
		return maps.EqualFunc(a, b, func(x, y map[string]int32) bool { return maps.Equal(x, y) })
	}
	if !equalRouting(target.Routing, acknowledged.Actual.Routing) || !equalRouting(target.Routing, snapshot.Routing) {
		return errors.New("complete acknowledged/current physical routing differs from staticRouting")
	}
	if m.Operation.Kind == operationRemove {
		for _, route := range target.Routing {
			if route[m.Operation.Instance] > 0 {
				return errors.New("remove target still receives owner routing; " +
					"restore staticRouting and await acknowledgement first")
			}
		}
		for _, request := range snapshot.Outstanding {
			if request.Instance == m.Operation.Instance {
				return errors.New("owner still owns accepted execution/transport/response work on remove target")
			}
		}
	}
	if m.Operation.Kind != operationRemove {
		for _, instance := range snapshot.Instances {
			if instance.Node == m.Operation.Node {
				return errors.New("target still has an owner-observed serving instance")
			}
		}
	}
	return nil
}

func bindExecutionPolicy(ctx context.Context, reader evidencestore.Reader, spec *servingkube.PrepareSpec) error {
	if spec.CompletionCalibration == nil {
		return nil // Preserve configured bootstrap policy; do not claim calibration.
	}
	facts, err := serving.LoadCompletionCalibration(ctx, reader, *spec.CompletionCalibration, 64<<20)
	if err != nil {
		return err
	}
	instance := &spec.Instance
	selected := ""
	for id, fact := range facts {
		if !fact.Matches(*instance) || (selected != "" && (fact.Completion < instance.CompletionOverhead ||
			fact.Completion == instance.CompletionOverhead && id >= selected)) {
			continue
		}
		instance.CompletionOverhead, instance.CompletionMeasurement = fact.Completion, &fact
		instance.CompletionObserved, instance.EnvelopeSource = true, fact.Source
		selected = id
	}
	if selected == "" {
		return errors.New("selected immutable completion calibration lacks the configured measured execution condition")
	}
	spec.Config.Policy = native.Policy{
		DecisionID: spec.Config.Policy.DecisionID, ProfileID: instance.Profile.ID,
		DeadlinePolicy: native.EffectiveDeadlinePolicy(instance.Profile.Condition.Firstfloor.DeadlinePolicy),
		MaxBatchSize:   instance.MaxBatch, MaxBatchWaitMS: float64(instance.MaxWait) / float64(time.Millisecond),
		CompletionOverheadMS: float64(instance.CompletionOverhead) / float64(time.Millisecond),
		EnvelopeSource:       instance.CompletionPolicySource(),
	}
	return nil
}

func validateExecutionNode(
	ctx context.Context, kube kubernetes.Interface, m executionManifest, spec servingkube.PrepareSpec,
	inventory []serving.SchedulerNode,
) error {
	node, err := kube.CoreV1().Nodes().Get(ctx, m.Operation.Node.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if string(node.UID) != m.Operation.Node.UID || node.DeletionTimestamp != nil ||
		node.Labels[servingkube.ServingPoolLabel] != m.ExpectedPool {
		return errors.New("actual Node identity or expected initial pool changed")
	}
	if m.Operation.Kind == operationMembership {
		if m.Operation.Pool == m.ExpectedPool ||
			(m.Operation.Pool != "" && m.Operation.Pool != spec.Instance.Pool) ||
			(m.ExpectedPool != "" && m.ExpectedPool != spec.Instance.Pool) {
			return errors.New("membership may only change between detached and the configured preparation pool")
		}
	} else if m.ExpectedPool != spec.Instance.Pool {
		return errors.New("actual pool differs from configured preparation pool")
	}
	pods, err := kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	if m.Operation.Kind == operationRemove {
		return validateExecutionRemoval(pods.Items, m, spec)
	}
	if m.Operation.Kind == operationPrepare {
		for _, nodeInventory := range inventory {
			if nodeInventory.Node == spec.Instance.Node && nodeInventory.Pool == spec.Instance.Pool {
				return servingkube.ValidatePreparationCapacity(node, pods, nodeInventory, spec.Instance)
			}
		}
		return errors.New("preparation Node inventory is absent from the acknowledged fresh owner snapshot")
	}
	for _, pod := range pods.Items {
		if pod.Spec.NodeName != node.Name {
			continue
		}
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		if pod.Labels["app.kubernetes.io/component"] == "serving-instance" {
			return fmt.Errorf("membership target has an active serving Pod: %s/%s", pod.Namespace, pod.Name)
		}
	}
	return nil
}

func validateExecutionRemoval(pods []corev1.Pod, m executionManifest, spec servingkube.PrepareSpec) error {
	for _, pod := range pods {
		if pod.Spec.NodeName != m.Operation.Node.Name {
			continue
		}
		for _, status := range pod.Status.ContainerStatuses {
			if string(pod.UID)+"/"+status.ContainerID != m.Operation.Instance || status.Name != "firstfloor" {
				continue
			}
			if pod.Namespace != m.Namespace || pod.Name != spec.Name || len(pod.OwnerReferences) != 0 ||
				pod.Annotations[servingkube.ServingProfileAnnotation] != m.ProfileID ||
				pod.Annotations[servingkube.ServingSlotAnnotation] != spec.Instance.ID {
				return errors.New("remove source is not the exact configured unowned preparation")
			}
			return nil
		}
	}
	return errors.New("exact configured remove source is unavailable")
}

func runExecution(
	ctx context.Context, m executionManifest, options kubeconfig.Options, timeout time.Duration,
) (retErr error) {
	op := m.Operation
	if err := m.validate(); err != nil {
		return err
	}
	ownerClient, err := executionOwnerClient(m)
	if err != nil {
		return err
	}
	config, err := kubeconfig.BuildConfig(options)
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
	selected, err := readExecutionConfiguration(ctx, kube, m)
	if err != nil {
		return err
	}
	spec, err := selectExecutionPreparation(ctx, ownerClient, m, selected)
	if err != nil {
		return err
	}
	objects, err := stores3.New(stores3.Config{
		LogicalStore: spec.Store.Store, Endpoint: spec.Store.Endpoint,
		Bucket: spec.Store.Bucket, Region: spec.Store.Region,
		AccessKeyID: os.Getenv("AWS_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		SessionToken: os.Getenv("AWS_SESSION_TOKEN"),
	})
	if err != nil {
		return err
	}
	profiles, err := bindExecutionCatalog(ctx, store, objects, selected, m, &spec)
	if err != nil {
		return err
	}
	if op.Kind == operationPrepare {
		if err = bindExecutionBundle(ctx, objects, &spec); err != nil {
			return err
		}
		op.Prepare = &spec
	}
	ownership, err := serving.AcquireObservationOwnership(m.JournalDirectory)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, ownership.Close()) }()
	journal, err := serving.NewJournal(serving.JournalConfig{
		Directory: m.JournalDirectory, Store: spec.Store.Store, Window: time.Minute, Writer: objects,
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
		return errors.New("execute journal contains outstanding traffic; use an exclusive clean observation directory")
	}
	nativeClient := &sdk.Client{HTTPClient: &http.Client{Timeout: timeout}}
	observer, err := servingkube.NewObserver(store, kube, execution, nativeClient, servingkube.ObserverConfig{
		Namespace: m.Namespace, ActuationNamespace: m.ActuationNamespace,
	})
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, observer.Close()) }()
	observer.SetProfiles(profiles)
	if err = observer.Refresh(ctx); err != nil {
		return err
	}
	operations, err := servingkube.NewOperations(store, kube, execution, observer, nativeClient,
		servingkube.OperationConfig{
			Namespace: m.Namespace, ActuationNamespace: m.ActuationNamespace, PollInterval: 100 * time.Millisecond,
		})
	if err != nil {
		return err
	}
	// A standalone runtime does NOT stop admission in the live owner. Require
	// that owner's acknowledged route exclusion and full accepted-call drain.
	acknowledged, err := readExecutionAcknowledgement(ctx, ownerClient, m, time.Now().UTC())
	if err != nil {
		return err
	}
	var owner executionOwnerView
	if err = readExecutionOwner(ctx, ownerClient, m.OwnerURL, "/api/v1/scheduler", &owner); err != nil {
		return err
	}
	if err = validateExecutionOwner(m, selected, owner, acknowledged, time.Now().UTC()); err != nil {
		return err
	}
	if err = validateExecutionNode(ctx, kube, m, spec, owner.State.Snapshot.Nodes); err != nil {
		return err
	}
	// Native resourceVersion is re-read immediately before physical execution;
	// do not turn a stale owner/configuration witness into an ownership bypass.
	if _, err = readExecutionConfiguration(ctx, kube, m); err != nil {
		return err
	}
	op.SourceConfigRV = m.SourceConfigRV
	result, executeErr := operations.Execute(ctx, op)
	return errors.Join(executeErr, json.NewEncoder(os.Stdout).Encode(result))
}

func (m executionManifest) validate() error {
	op := m.Operation
	if m.Namespace == "" || m.JournalDirectory == "" || m.SchedulerConfigMap == "" || m.SchedulerConfigKey == "" ||
		m.SourceConfigRV == "" || m.RoutingHistoryFrom.IsZero() ||
		m.ProfileID == "" || op.ID == "" || op.Node.Name == "" || op.Node.UID == "" ||
		(op.Kind != operationPrepare && op.Kind != operationRemove && op.Kind != operationMembership) ||
		op.Prepare != nil || op.Target != nil || op.Actuation != nil || op.StaticRouting != nil || op.SourceConfigRV != "" ||
		(op.Kind == operationRemove) != (op.Instance != "") || (op.Kind != operationMembership && op.Pool != "") {
		return errors.New("bounded execute manifest requires exact scope and only configured " +
			"prepare, exact remove or serving-pool membership")
	}
	return nil
}

func selectExecutionPreparation(
	ctx context.Context, ownerClient *http.Client, m executionManifest, config executionConfiguration,
) (servingkube.PrepareSpec, error) {
	var selections entrypoint.ProfileSelectionsView
	if err := readExecutionOwner(ctx, ownerClient, m.OwnerURL, "/api/v1/scheduler/profiles", &selections); err != nil {
		return servingkube.PrepareSpec{}, err
	}
	loaded := false
	for _, selection := range selections.Selections {
		loaded = loaded || (selection.ProfileID == m.ProfileID && selection.Loaded && selection.Persisted &&
			selection.ConfigVersion == m.SourceConfigRV)
	}
	spec, configured := config.Preparations[m.ProfileID]
	if !loaded || !configured || selections.ConfigVersion != m.SourceConfigRV || spec.Instance.Profile != nil ||
		spec.Instance.Node != m.Operation.Node {
		return spec, errors.New("preparation/profile must equal the current owner-loaded configured binding")
	}
	return spec, spec.Store.Validate()
}

func bindExecutionCatalog(
	ctx context.Context, store *servingkube.Store, objects *stores3.Store,
	config executionConfiguration, m executionManifest, spec *servingkube.PrepareSpec,
) ([]serving.Profile, error) {
	catalog, err := servingkube.NewCatalog(store, objects, 64<<20, 0.95)
	if err != nil {
		return nil, err
	}
	campaigns := make([]types.NamespacedName, len(config.ProfileCampaigns))
	for i, campaign := range config.ProfileCampaigns {
		campaigns[i] = types.NamespacedName{Namespace: campaign.Namespace, Name: campaign.Name}
	}
	view, err := catalog.Refresh(ctx, campaigns)
	if err != nil {
		return nil, err
	}
	spec.Instance.Profile = &serving.Profile{ID: m.ProfileID}
	if err = bindMeasurementProfile(spec, view.Profiles); err != nil {
		return nil, err
	}
	profile := spec.Instance.Profile
	if profile.NodeName != m.Operation.Node.Name || profile.Condition.NodeUID != m.Operation.Node.UID ||
		profile.Condition.Backend != "ort-cpu" || profile.RuntimeMetadata == nil || profile.RuntimeMetadata.GPUUUID != "" {
		return nil, errors.New("execute mode requires the configured measured CPU profile on the exact physical Node")
	}
	return view.Profiles, nil
}

func bindExecutionBundle(ctx context.Context, objects evidencestore.Reader, spec *servingkube.PrepareSpec) error {
	if err := bindExecutionPolicy(ctx, objects, spec); err != nil {
		return err
	}
	profile := spec.Instance.Profile
	bundle := profile.NativeBundle()
	raw, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	if len(raw) > 64<<20 {
		return errors.New("catalog-derived immutable profile bundle exceeds bound")
	}
	spec.Bundle = evidencestore.ObjectIdentity{
		Store: spec.Store.Store, Key: "serving/profiles/" + profile.ID + "/bundle.json", Size: int64(len(raw)),
	}
	spec.Config.Mode, spec.Config.ProfilePath = "serving", "/profile/bundle.json"
	spec.Config.Policy.ProfileID, spec.Config.Policy.Batches = profile.ID, nil
	checked := spec.Config
	checked.Policy.Batches = bundle.Batches
	return native.ValidateConfig(&checked)
}

func readExecutionAcknowledgement(
	ctx context.Context, ownerClient *http.Client, m executionManifest, to time.Time,
) (*servingkube.OperationResult, error) {
	if m.RoutingHistoryFrom.IsZero() || !m.RoutingHistoryFrom.Before(to) {
		return nil, errors.New("routingHistoryFrom must select an explicit positive owner history interval")
	}
	query := url.Values{
		"from": {m.RoutingHistoryFrom.UTC().Format(time.RFC3339Nano)},
		"to":   {to.UTC().Format(time.RFC3339Nano)},
	}
	var latest *servingkube.OperationResult
	for {
		var page struct {
			Events     []serving.SchedulerObservation `json:"events"`
			NextCursor string                         `json:"nextCursor"`
		}
		if err := readExecutionOwner(ctx, ownerClient, m.OwnerURL,
			"/api/v1/observations/scheduler?"+query.Encode(), &page); err != nil {
			return nil, err
		}
		for _, event := range page.Events {
			if event.Action == nil || event.Action.Kind != manualStaticRouting || len(event.Action.Actual) == 0 {
				continue
			}
			var result servingkube.OperationResult
			if err := json.Unmarshal(event.Action.Actual, &result); err != nil {
				return nil, err
			}
			if result.Operation.SourceConfigRV == m.SourceConfigRV &&
				(latest == nil || result.EndedAt.After(latest.EndedAt)) {
				latest = &result
			}
		}
		if page.NextCursor == "" {
			break
		}
		query.Set("cursor", page.NextCursor)
	}
	if latest == nil {
		return nil, errors.New("selected owner history has no manual staticRouting acknowledgement for this revision")
	}
	return latest, nil
}
