package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/lab-paper-code/chill/internal/kubeconfig"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/nodeprobe"
)

func main() {
	var nodeName string
	var hostRoot string
	var signatureFile string
	var interval time.Duration
	var once bool
	var cleanupOnExit bool
	var cleanupTimeout time.Duration
	var kubeAPIServer string
	var kubeAPITokenFile string
	var kubeAPICAFile string

	flag.StringVar(&nodeName, "node-name", os.Getenv("NODE_NAME"), "Kubernetes Node name to patch.")
	flag.StringVar(&hostRoot, "host-root", "/host", "Root path containing read-only host mounts.")
	flag.StringVar(
		&signatureFile,
		"signature-file",
		"/etc/chill/node-discovery/signatures.yaml",
		"YAML file containing node discovery signatures.",
	)
	flag.DurationVar(&interval, "interval", 10*time.Minute, "How often to refresh node discovery metadata.")
	flag.BoolVar(&once, "once", false, "Run one discovery pass and exit.")
	flag.BoolVar(&cleanupOnExit, "cleanup-on-exit", false,
		"Remove CHILL-managed labels from this Node before exiting on signal.")
	flag.DurationVar(&cleanupTimeout, "cleanup-timeout", 10*time.Second, "Maximum time allowed for exit cleanup.")
	flag.StringVar(&kubeAPIServer, "kube-api-server", os.Getenv("CHILL_KUBE_API_SERVER"),
		"Kubernetes API server URL. Leave empty to use standard in-cluster config.")
	flag.StringVar(&kubeAPITokenFile, "kube-api-token-file", kubeconfig.DefaultServiceAccountTokenFile,
		"Path to the service account bearer token file.")
	flag.StringVar(&kubeAPICAFile, "kube-api-ca-file", kubeconfig.DefaultServiceAccountCAFile,
		"Path to the Kubernetes API CA file.")
	flag.Parse()

	if nodeName == "" {
		log.Fatal("node name is required; set NODE_NAME or --node-name")
	}

	config, err := kubeconfig.BuildConfig(kubeconfig.Options{
		APIServer: kubeAPIServer,
		TokenFile: kubeAPITokenFile,
		CAFile:    kubeAPICAFile,
	})
	if err != nil {
		log.Fatalf("load Kubernetes client config: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("create kubernetes client: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		if ctx.Err() != nil {
			cleanupOnSignal(cleanupOnExit, cleanupTimeout, clientset, nodeName)
			return
		}
		if err := runOnce(ctx, clientset, nodeName, hostRoot, signatureFile); err != nil {
			log.Printf("node discovery failed: %v", err)
		}
		if once {
			return
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			cleanupOnSignal(cleanupOnExit, cleanupTimeout, clientset, nodeName)
			return
		case <-timer.C:
		}
	}
}

func runOnce(ctx context.Context, clientset kubernetes.Interface, nodeName, hostRoot, signatureFile string) error {
	catalog, err := nodeprobe.LoadSignatureCatalog(signatureFile)
	if err != nil {
		return fmt.Errorf("load node discovery signatures: %w", err)
	}

	facts, err := nodeprobe.Probe(hostRoot, catalog)
	if err != nil {
		return fmt.Errorf("probe host: %w", err)
	}

	labels := facts.Labels()
	annotations := facts.Annotations()
	if len(labels) == 0 && len(annotations) == 0 {
		log.Printf("no known device facts discovered for node %q", nodeName)
		return nil
	}

	node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("node %q not found", nodeName)
		}
		return fmt.Errorf("get node %q: %w", nodeName, err)
	}

	patch, changed, err := buildNodePatch(node, labels, annotations)
	if err != nil {
		return err
	}
	if !changed {
		log.Printf("node %q discovery labels already up to date", nodeName)
		return nil
	}

	if _, err := clientset.CoreV1().Nodes().Patch(
		ctx,
		nodeName,
		types.MergePatchType,
		patch,
		metav1.PatchOptions{},
	); err != nil {
		return fmt.Errorf("patch node %q: %w", nodeName, err)
	}
	log.Printf("patched node %q with discovery labels=%v annotations=%v", nodeName, labels, annotations)
	return nil
}

func cleanupOnSignal(cleanupOnExit bool, timeout time.Duration, clientset kubernetes.Interface, nodeName string) {
	if !cleanupOnExit {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := cleanupNodeMetadata(ctx, clientset, nodeName); err != nil {
		log.Printf("node discovery cleanup failed: %v", err)
	}
}

func cleanupNodeMetadata(ctx context.Context, clientset kubernetes.Interface, nodeName string) error {
	node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get node %q: %w", nodeName, err)
	}

	patch, changed, err := buildNodeCleanupPatch(node)
	if err != nil {
		return err
	}
	if !changed {
		log.Printf("node %q has no CHILL-managed discovery metadata to clean", nodeName)
		return nil
	}

	if _, err := clientset.CoreV1().Nodes().Patch(
		ctx,
		nodeName,
		types.MergePatchType,
		patch,
		metav1.PatchOptions{},
	); err != nil {
		return fmt.Errorf("patch node %q cleanup metadata: %w", nodeName, err)
	}
	log.Printf("cleaned CHILL-managed discovery metadata from node %q", nodeName)
	return nil
}

func buildNodePatch(node *corev1.Node, labels, annotations map[string]string) ([]byte, bool, error) {
	patchLabels := map[string]string{}
	patchAnnotations := map[string]string{}
	for key, value := range labels {
		if node.Labels[key] != value {
			patchLabels[key] = value
		}
	}
	for key, value := range annotations {
		if node.Annotations[key] != value {
			patchAnnotations[key] = value
		}
	}

	if len(patchLabels) == 0 && len(patchAnnotations) == 0 {
		return nil, false, nil
	}

	metadata := map[string]any{}
	if len(patchLabels) > 0 {
		metadata["labels"] = patchLabels
	}
	if len(patchAnnotations) > 0 {
		metadata["annotations"] = patchAnnotations
	}

	payload := map[string]any{
		"metadata": metadata,
	}
	patch, err := json.Marshal(payload)
	if err != nil {
		return nil, false, fmt.Errorf("marshal node patch: %w", err)
	}
	return patch, true, nil
}

func buildNodeCleanupPatch(node *corev1.Node) ([]byte, bool, error) {
	patchLabels := map[string]*string{}
	patchAnnotations := map[string]*string{}
	labels := node.GetLabels()
	annotations := node.GetAnnotations()

	if annotations[chillmeta.DiscoverySource] == chillmeta.SourceNodeDiscovery {
		addDeleteKeys(patchLabels,
			chillmeta.DeviceVendor,
			chillmeta.DeviceFamily,
			chillmeta.DeviceModel,
			chillmeta.Accelerator,
		)
		addDeleteKeys(patchAnnotations,
			chillmeta.DeviceModelRaw,
			chillmeta.DiscoverySource,
			chillmeta.NodeDiscoveryResult,
			chillmeta.NodeDiscoveryReason,
		)
	}

	if annotations[chillmeta.ManagedBy] == chillmeta.ManagedByDeviceDiscovery {
		addDeleteKeys(patchLabels, chillmeta.DeviceClass)
		addDeleteKeys(patchAnnotations, chillmeta.ManagedBy)
	}

	addDeleteKeys(patchAnnotations,
		chillmeta.DeviceClassDiscoveryResult,
		chillmeta.DeviceClassDiscoveryReason,
		chillmeta.DeviceClassDiscoveryClass,
	)

	pruneAbsentDeleteKeys(patchLabels, labels)
	pruneAbsentDeleteKeys(patchAnnotations, annotations)
	if len(patchLabels) == 0 && len(patchAnnotations) == 0 {
		return nil, false, nil
	}

	metadata := map[string]any{}
	if len(patchLabels) > 0 {
		metadata["labels"] = patchLabels
	}
	if len(patchAnnotations) > 0 {
		metadata["annotations"] = patchAnnotations
	}
	patch, err := json.Marshal(map[string]any{"metadata": metadata})
	if err != nil {
		return nil, false, fmt.Errorf("marshal node cleanup patch: %w", err)
	}
	return patch, true, nil
}

func addDeleteKeys(values map[string]*string, keys ...string) {
	for _, key := range keys {
		values[key] = nil
	}
}

func pruneAbsentDeleteKeys(deleteKeys map[string]*string, existing map[string]string) {
	for key := range deleteKeys {
		if _, ok := existing[key]; !ok {
			delete(deleteKeys, key)
		}
	}
}
