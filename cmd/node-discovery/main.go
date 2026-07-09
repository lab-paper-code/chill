package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/lab-paper-code/chill/internal/nodediscovery"
)

func main() {
	var nodeName string
	var hostRoot string
	var signatureFile string
	var interval time.Duration
	var once bool

	flag.StringVar(&nodeName, "node-name", os.Getenv("NODE_NAME"), "Kubernetes Node name to patch.")
	flag.StringVar(&hostRoot, "host-root", "/host", "Root path containing read-only host mounts.")
	flag.StringVar(
		&signatureFile,
		"signature-file",
		"/etc/chill/node-discovery/signatures.yaml",
		"YAML file containing node discovery signatures.",
	)
	flag.DurationVar(&interval, "interval", 10*time.Minute, "How often to refresh node discovery labels.")
	flag.BoolVar(&once, "once", false, "Run one discovery pass and exit.")
	flag.Parse()

	if nodeName == "" {
		log.Fatal("node name is required; set NODE_NAME or --node-name")
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("load in-cluster config: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("create kubernetes client: %v", err)
	}

	ctx := context.Background()
	for {
		if err := runOnce(ctx, clientset, nodeName, hostRoot, signatureFile); err != nil {
			log.Printf("node discovery failed: %v", err)
		}
		if once {
			return
		}
		time.Sleep(interval)
	}
}

func runOnce(ctx context.Context, clientset kubernetes.Interface, nodeName, hostRoot, signatureFile string) error {
	catalog, err := nodediscovery.LoadSignatureCatalog(signatureFile)
	if err != nil {
		return fmt.Errorf("load node discovery signatures: %w", err)
	}

	facts, err := nodediscovery.Probe(hostRoot, catalog)
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
