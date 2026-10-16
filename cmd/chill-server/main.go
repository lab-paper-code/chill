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
	log := ctrl.Log.WithName("chill-server")

	if err := opts.validate(); err != nil {
		log.Error(err, "invalid ChillServer options")
		os.Exit(1)
	}
	if opts.materializeProfile {
		log.Info("materializing immutable startup profile", "objectKey", opts.profileObjectKey, "output", opts.profileOutput)
	} else {
		log.Info("starting ChillServer",
			"bindAddress", opts.bindAddress,
			"servingNamespace", opts.servingNamespace,
			"profileNamespaces", opts.profileNamespaces,
			"schedulerConfigMap", opts.schedulerConfigMap)
	}
	if err := run(ctrl.SetupSignalHandler(), opts); err != nil {
		log.Error(err, "run ChillServer")
		os.Exit(1)
	}
}
