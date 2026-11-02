package main

import (
	"flag"
	"os"

	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"k8s.io/client-go/kubernetes"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func main() {
	opts := options{}
	opts.bindFlags(flag.CommandLine)
	zapOptions := zap.Options{Development: true}
	zapOptions.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOptions)))
	log := ctrl.Log.WithName("chill-query")

	if err := opts.validate(); err != nil {
		log.Error(err, "invalid Query API options")
		os.Exit(1)
	}
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(edgev1alpha1.AddToScheme(scheme))
	config := ctrl.GetConfigOrDie()
	config.QPS, config.Burst = float32(opts.kubeAPIQPS), opts.kubeAPIBurst
	reader, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "construct Kubernetes reader")
		os.Exit(1)
	}
	reviews, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Error(err, "construct query authorization client")
		os.Exit(1)
	}
	handler, err := buildHandler(reader, opts, &servingkube.Authorizer{Client: reviews})
	if err != nil {
		log.Error(err, "construct Query API HTTP handler")
		os.Exit(1)
	}
	log.Info("starting Query API read API", "bindAddress", opts.bindAddress)
	if err := serve(ctrl.SetupSignalHandler(), opts, handler); err != nil {
		log.Error(err, "run Query API read API")
		os.Exit(1)
	}
}
