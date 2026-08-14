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
	routes, err := readRoutes(opts.routesPath)
	if err != nil {
		log.Error(err, "read the routing table", "path", opts.routesPath)
		os.Exit(1)
	}
	log.Info("starting the inference entry point",
		"bindAddress", opts.bindAddress,
		"servingNamespace", opts.servingNamespace,
		"servingPort", opts.servingPort,
		"models", routes.modelNames())
	if err := serve(ctrl.SetupSignalHandler(), opts, buildHandler(routes, opts)); err != nil {
		log.Error(err, "run the inference entry point")
		os.Exit(1)
	}
}
