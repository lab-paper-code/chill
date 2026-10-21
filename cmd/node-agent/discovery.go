package main

import (
	"context"
	"encoding/json"
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

// runDiscovery publishes the Node's discovery metadata every interval and
// patches only on change.
func runDiscovery(opts options) {
	if opts.nodeName == "" {
		log.Fatal("node name is required; set NODE_NAME or --node-name")
	}
	config, err := kubeconfig.BuildConfig(kubeconfig.Options{
		APIServer: opts.kubeAPIServer,
		TokenFile: opts.kubeAPITokenFile,
		CAFile:    opts.kubeAPICAFile,
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
			return
		}
		if err := runOnce(ctx, clientset, opts.nodeName, opts.systemName, opts.hostRoot, opts.signatureFile); err != nil {
			log.Printf("node discovery failed: %v", err)
		}
		if opts.once {
			return
		}
		timer := time.NewTimer(opts.interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
	}
}

func runOnce(
	ctx context.Context,
	clientset kubernetes.Interface,
	nodeName,
	systemName,
	hostRoot,
	signatureFile string,
) error {
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
	if systemName != "" {
		annotations[chillmeta.System] = systemName
	}
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
