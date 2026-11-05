// Command pretrial-retirement-check emits read-only observations for the reviewed
// legacy pre-trial retirement boundary. It never changes Kubernetes or S3 objects.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign/controller"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Stdout))
}

func run(ctx context.Context, args []string, out io.Writer) int {
	flags := flag.NewFlagSet("pretrial-retirement-check", flag.ContinueOnError)
	// Flag diagnostics can echo untrusted argv. Emit only bounded JSON errors.
	flags.SetOutput(io.Discard)
	var kubeconfig, kubeContext, namespace, system, name, uid string
	flags.StringVar(&kubeconfig, "kubeconfig", "", "explicit kubeconfig path")
	flags.StringVar(&kubeContext, "context", "", "explicit kubeconfig context")
	flags.StringVar(&namespace, "namespace", "", "request namespace")
	flags.StringVar(&system, "system-name", "", "owning ChillSystem name")
	flags.StringVar(&name, "name", "", "BatchEnergyOptimization name")
	flags.StringVar(&uid, "uid", "", "exact BatchEnergyOptimization UID")
	proof := struct {
		campaign.PretrialFailureInspection
		Error string `json:"error,omitempty"`
	}{PretrialFailureInspection: campaign.PretrialFailureInspection{
		APIVersion: "chill.dacs.io/pretrial-retirement.v1",
		Reason:     "InspectionFailed",
		CheckedAt:  time.Now().UTC(),
	}}
	finish := func(detail string) int {
		if detail != "" {
			proof.Eligible = false
			proof.Error = detail
		}
		if err := json.NewEncoder(out).Encode(proof); err != nil {
			return 1
		}
		if proof.Eligible {
			return 0
		}
		return 1
	}
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return finish("invalid arguments; require --kubeconfig --context --namespace --system-name --name --uid")
	}
	for _, value := range []string{kubeconfig, kubeContext, namespace, system, name, uid} {
		if value == "" || strings.TrimSpace(value) != value {
			return finish("all six flags are required without surrounding whitespace")
		}
	}
	proof.Request = campaign.PretrialInspectionRequest{Namespace: namespace, Name: name, UID: types.UID(uid)}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
	).ClientConfig()
	if err != nil {
		return finish("load explicit kubeconfig/context failed")
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return finish("register Kubernetes scheme failed")
	}
	if err := edge.AddToScheme(scheme); err != nil {
		return finish("register CHILL scheme failed")
	}
	reader, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return finish("construct Kubernetes reader failed")
	}
	resolver, err := profilerkube.NewKubernetesEvidenceStoreResolver(reader, profilerkube.EvidenceStoreResolverOptions{SystemName: system, Namespace: namespace})
	if err != nil {
		return finish("construct configured Evidence Store resolver failed")
	}
	binding, err := resolver.Resolve(ctx)
	if err != nil {
		return finish("resolve configured Evidence Store failed")
	}
	logicalStore := component.EvidenceStoreServiceName(system)
	if binding.Store != logicalStore {
		return finish("configured Evidence Store identity mismatch")
	}
	credentials, err := storekube.LoadCredentials(ctx, reader, namespace, binding.CredentialsSecretName)
	if err != nil {
		return finish("load configured Evidence Store credentials failed")
	}
	objects, err := stores3.New(stores3.Config{
		LogicalStore: binding.Store, Bucket: binding.Bucket, Endpoint: binding.Endpoint, Region: binding.Region,
		AccessKeyID: credentials.AccessKeyID, SecretAccessKey: credentials.SecretAccessKey,
		TempDir: component.ProfilerEvidenceStagingPath,
	})
	if err != nil {
		return finish("construct configured Evidence Store reader failed")
	}
	inspection, inspectErr := campaign.InspectPretrialFailure(ctx, reader, objects, logicalStore, types.NamespacedName{Namespace: namespace, Name: name}, types.UID(uid))
	proof.PretrialFailureInspection = inspection
	if inspectErr != nil {
		// API and SDK errors may contain endpoints, credentials, or server bodies.
		return finish("read-only pre-trial inspection failed")
	}
	return finish("")
}
