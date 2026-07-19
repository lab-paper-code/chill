package main

import (
	"flag"
	"os"

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
	log := ctrl.Log.WithName("console")

	if err := opts.validate(); err != nil {
		log.Error(err, "invalid Console options")
		os.Exit(1)
	}
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(edgev1alpha1.AddToScheme(scheme))
	reader, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "construct Kubernetes reader")
		os.Exit(1)
	}
	handler, err := buildHandler(reader, opts)
	if err != nil {
		log.Error(err, "construct Console API")
		os.Exit(1)
	}
	log.Info("starting Console read API", "bindAddress", opts.bindAddress)
	if err := serve(ctrl.SetupSignalHandler(), opts, handler); err != nil {
		log.Error(err, "run Console read API")
		os.Exit(1)
	}
}
