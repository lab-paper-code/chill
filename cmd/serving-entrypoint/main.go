package main

import (
	"flag"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func main() {
	opts := options{}
	opts.bindFlags(flag.CommandLine)
	zapOptions := zap.Options{Development: true}
	zapOptions.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOptions)))
	log := ctrl.Log.WithName("serving-entrypoint")

	if err := opts.validate(); err != nil {
		log.Error(err, "invalid inference entry point options")
		os.Exit(1)
	}
	if opts.materializeProfile {
		log.Info("materializing immutable startup profile", "objectKey", opts.profileObjectKey, "output", opts.profileOutput)
	} else {
		log.Info("starting the Workload owner",
			"bindAddress", opts.bindAddress,
			"servingNamespace", opts.servingNamespace,
			"profileNamespaces", opts.profileNamespaces,
			"schedulerConfigMap", opts.schedulerConfigMap)
	}
	if err := run(ctrl.SetupSignalHandler(), opts); err != nil {
		log.Error(err, "run the inference entry point")
		os.Exit(1)
	}
}
