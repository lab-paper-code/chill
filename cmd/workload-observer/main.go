package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/client-go/kubernetes"

	"github.com/lab-paper-code/chill/internal/kubeconfig"
)

func main() {
	opts := options{
		podNamespace: os.Getenv("POD_NAMESPACE"),
		podName:      os.Getenv("POD_NAME"),
		podUID:       os.Getenv("POD_UID"),
	}
	opts.bindFlags(flag.CommandLine)
	flag.Parse()
	if err := opts.validate(); err != nil {
		log.Printf("invalid WorkloadObserver options: %v", err)
		os.Exit(2)
	}

	config, err := kubeconfig.BuildConfig(kubeconfig.Options{
		APIServer: opts.kubeAPIServer,
		TokenFile: opts.kubeAPITokenFile,
		CAFile:    opts.kubeAPICAFile,
	})
	if err != nil {
		log.Printf("load Kubernetes client config: %v", err)
		os.Exit(3)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Printf("create Kubernetes client: %v", err)
		os.Exit(3)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx, opts, clientset.CoreV1(), signalPollInterval); err != nil {
		log.Printf("WorkloadObserver failed: %v", err)
		os.Exit(4)
	}
}
