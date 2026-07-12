package main

// TODO(internal): Keep observation mechanics in a reusable package, but move
// request creation, Node-to-source resolution, retries, Run identity, and
// status persistence into the profiler controller. This CLI remains a bounded
// sidecar/probe entry point rather than a reconciler.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/lab-paper-code/chill/internal/powerobserver"
	"github.com/lab-paper-code/chill/internal/powerobserver/edgemetrics"
	powerobserverprobe "github.com/lab-paper-code/chill/spikes/power-observer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	nodeName := flag.String("node-name", "", "Kubernetes Node to observe")
	resolvedEndpoint := flag.String("resolved-endpoint", "", "endpoint already resolved by the profiling orchestrator")
	namespace := flag.String("namespace", "monitoring", "namespace containing edge-metrics exporter Pods")
	selector := flag.String(
		"selector",
		"app.kubernetes.io/name=exporter,app.kubernetes.io/instance=edge-metrics",
		"exporter Pod label selector",
	)
	port := flag.Int("port", 9102, "exporter metrics port")
	kubeconfig := flag.String(
		"kubeconfig", "", "kubeconfig path; defaults to in-cluster config or normal client loading rules",
	)
	interval := flag.Duration("interval", time.Second, "polling interval")
	duration := flag.Duration("duration", 30*time.Second, "observation duration")
	requestTimeout := flag.Duration("request-timeout", 500*time.Millisecond, "timeout per source request")
	readyFile := flag.String("ready-file", "", "optional file created after target resolution")
	startSignalFile := flag.String("start-signal-file", "", "optional file that gates observation start")
	startSignalTimeout := flag.Duration(
		"start-signal-timeout", 30*time.Second, "maximum wait for the workload measurement signal",
	)
	flag.Parse()
	if *nodeName == "" {
		fmt.Fprintln(os.Stderr, "-node-name is required")
		os.Exit(2)
	}

	var target powerobserverprobe.ResolvedTarget
	if *resolvedEndpoint != "" {
		// TODO(internal): Accept a controller-resolved PowerSource reference and
		// credentials/TLS policy, not a raw endpoint in the public profiling API.
		logLine(
			"INFO", "target", "using endpoint resolved by profiling control",
			"node", *nodeName, "endpoint", *resolvedEndpoint,
		)
		target = powerobserverprobe.ResolvedTarget{NodeName: *nodeName, Endpoint: *resolvedEndpoint}
	} else {
		// TODO(internal): Decide one owner for resolution. The production sidecar
		// should not silently fall back to Kubernetes discovery if the controller
		// is responsible for freezing the source used by an immutable Run.
		logLine("INFO", "target", "resolving edge-metrics endpoint from Kubernetes", "node", *nodeName)
		config, err := kubernetesConfig(*kubeconfig)
		if err != nil {
			fatal(err)
		}
		client, err := kubernetes.NewForConfig(config)
		if err != nil {
			fatal(fmt.Errorf("create Kubernetes client: %w", err))
		}
		resolver, err := powerobserverprobe.NewKubernetesPodResolver(client, *namespace, *selector, *port)
		if err != nil {
			fatal(err)
		}
		target, err = resolver.Resolve(context.Background(), *nodeName)
		if err != nil {
			fatal(err)
		}
	}
	targetFields := []string{"node", target.NodeName, "endpoint", target.Endpoint}
	if target.PodName != "" {
		targetFields = append(targetFields, "pod", target.PodName)
	}
	logLine("INFO", "target", "power source ready", targetFields...)
	source, err := edgemetrics.New(powerobserver.SourceIdentity{
		NodeName:  target.NodeName,
		Namespace: target.Namespace,
		PodName:   target.PodName,
		Endpoint:  target.Endpoint,
	}, http.DefaultClient)
	if err != nil {
		fatal(err)
	}
	observer, err := powerobserver.New(source)
	if err != nil {
		fatal(err)
	}
	if *readyFile != "" {
		if err := os.WriteFile(*readyFile, []byte("ready\n"), 0o644); err != nil {
			fatal(fmt.Errorf("write observer ready file: %w", err))
		}
		logLine("INFO", "sync", "published observer-ready signal", "path", *readyFile)
	}
	if *startSignalFile != "" {
		logLine("INFO", "sync", "waiting for workload measurement signal", "path", *startSignalFile)
		if err := waitForFile(context.Background(), *startSignalFile, *startSignalTimeout); err != nil {
			fatal(err)
		}
		logLine("INFO", "sync", "workload measurement signal received")
	}
	logLine(
		"INFO", "observe", "starting bounded power observation",
		"interval", interval.String(), "duration", duration.String(), "requestTimeout", requestTimeout.String(),
	)
	result, err := observer.Observe(context.Background(), powerobserver.Request{
		Interval: *interval, Duration: *duration, RequestTimeout: *requestTimeout,
	})
	if err != nil {
		fatal(err)
	}
	logObservationSummary(result)
	payload, err := json.Marshal(result)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("POWER_OBSERVATION_JSON %s\n", payload)
}

func waitForFile(ctx context.Context, path string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect start signal file: %w", err)
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("wait for start signal file %q: %w", path, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func kubernetesConfig(explicitPath string) (*rest.Config, error) {
	if explicitPath == "" {
		if config, err := rest.InClusterConfig(); err == nil {
			return config, nil
		}
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if explicitPath != "" {
		absolute, err := filepath.Abs(explicitPath)
		if err != nil {
			return nil, fmt.Errorf("resolve kubeconfig path: %w", err)
		}
		rules.ExplicitPath = absolute
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load Kubernetes config: %w", err)
	}
	return config, nil
}

func fatal(err error) {
	logLine("ERROR", "fatal", err.Error())
	os.Exit(1)
}

func logLine(level, stage, message string, fields ...string) {
	suffix := ""
	for index := 0; index+1 < len(fields); index += 2 {
		suffix += fmt.Sprintf(" %s=%q", fields[index], fields[index+1])
	}
	fmt.Fprintf(
		os.Stderr, "%s %-5s [%-8s] %s%s\n",
		time.Now().UTC().Format(time.RFC3339Nano), level, stage, message, suffix,
	)
}

func logObservationSummary(result powerobserver.Result) {
	watts := make([]float64, 0, len(result.Samples))
	for _, sample := range result.Samples {
		watts = append(watts, sample.Watts)
	}
	mean, minimum, maximum := 0.0, 0.0, 0.0
	if len(watts) > 0 {
		sort.Float64s(watts)
		minimum, maximum = watts[0], watts[len(watts)-1]
		for _, value := range watts {
			mean += value
		}
		mean /= float64(len(watts))
	}
	logLine("INFO", "observe", "power observation completed")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "--- CHILL POWER OBSERVATION RESULT ---------------------------")
	fmt.Fprintf(os.Stderr, "node                    : %s\n", result.Source.NodeName)
	fmt.Fprintf(os.Stderr, "endpoint                : %s\n", result.Source.Endpoint)
	fmt.Fprintf(os.Stderr, "metric                  : %s\n", result.Source.Metric)
	fmt.Fprintf(
		os.Stderr, "window                  : %s -> %s\n",
		result.StartedAt.UTC().Format(time.RFC3339Nano), result.EndedAt.UTC().Format(time.RFC3339Nano),
	)
	fmt.Fprintf(
		os.Stderr, "samples                 : %d successful / %d failed\n",
		result.Summary.SuccessfulSamples, result.Summary.Failures,
	)
	fmt.Fprintf(os.Stderr, "watts mean / min / max  : %.3f / %.3f / %.3f W\n", mean, minimum, maximum)
	fmt.Fprintf(os.Stderr, "request latency mean    : %.2f ms\n", result.Summary.MeanRequestLatencySeconds*1000)
	fmt.Fprintf(os.Stderr, "request latency p95     : %.2f ms\n", result.Summary.P95RequestLatencySeconds*1000)
	fmt.Fprintf(os.Stderr, "maximum sample gap      : %.3f s\n", result.Summary.MaximumSampleGapSeconds)
	fmt.Fprintln(os.Stderr, "sample timestamp        : observer receipt time")
	fmt.Fprintln(os.Stderr, "---------------------------------------------------------------")
}
