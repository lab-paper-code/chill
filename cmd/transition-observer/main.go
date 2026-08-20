package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"github.com/lab-paper-code/chill/internal/kubeconfig"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/metricobserver/edgemetrics"
	transitionkube "github.com/lab-paper-code/chill/internal/profiling/transitioncost/kube"
	transitionworker "github.com/lab-paper-code/chill/internal/profiling/transitioncost/worker"
)

type options struct {
	contractJSON, readyPath, terminationPath   string
	storeEndpoint, storeBucket, storeRegion    string
	tempDir                                    string
	accessKeyID, secretAccessKey, sessionToken string
	maxContractBytes                           int64
	probeReadyFile                             bool
}

func (o *options) bindFlags(set *flag.FlagSet) {
	set.StringVar(
		&o.readyPath, "ready-file", "/coordination/transition-observer-ready",
		"presence-only observation-ready file",
	)
	set.BoolVar(
		&o.probeReadyFile, "probe-ready-file", false,
		"exit successfully only when the observation-ready file exists",
	)
	set.StringVar(
		&o.terminationPath, "termination-message-file", "/dev/termination-log",
		"Kubernetes termination message path",
	)
	set.Int64Var(&o.maxContractBytes, "max-contract-bytes", 0, "Job-derived transition contract byte limit")
	set.StringVar(&o.storeEndpoint, "store-endpoint", "", "resolved Evidence Store endpoint")
	set.StringVar(&o.storeBucket, "store-bucket", "", "resolved evidence bucket")
	set.StringVar(&o.storeRegion, "store-region", "", "resolved Evidence Store region")
	set.StringVar(&o.tempDir, "temp-dir", "/workspace", "temporary evidence staging directory")
	o.contractJSON = os.Getenv("CHILL_TRANSITION_ATTEMPT_JSON")
	o.accessKeyID = os.Getenv("AWS_ACCESS_KEY_ID")
	o.secretAccessKey = os.Getenv("AWS_SECRET_ACCESS_KEY")
	o.sessionToken = os.Getenv("AWS_SESSION_TOKEN")
}

func (o options) validate() error {
	if err := validateAbsolutePath("ready", o.readyPath); err != nil {
		return err
	}
	if o.probeReadyFile {
		return nil
	}
	if o.contractJSON == "" {
		return errors.New("CHILL_TRANSITION_ATTEMPT_JSON is required")
	}
	if o.maxContractBytes < 1 {
		return errors.New("maximum contract bytes must be supplied by the Job")
	}
	if err := validateAbsolutePath("termination message", o.terminationPath); err != nil {
		return err
	}
	return nil
}

func validateAbsolutePath(name, path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return fmt.Errorf("%s path must be a clean absolute file path", name)
	}
	return nil
}

func main() {
	opts := options{}
	opts.bindFlags(flag.CommandLine)
	flag.Parse()
	if err := opts.validate(); err != nil {
		log.Printf("invalid transition-observer options: %v", err)
		os.Exit(2)
	}
	if opts.probeReadyFile {
		if err := probeReadyFile(opts.readyPath); err != nil {
			os.Exit(1)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	deps := dependencies{
		load: loadContract, build: buildComponents,
		observe: transitionworker.Observe, writeRaw: transitionworker.WriteRaw,
	}
	if err := execute(ctx, opts, deps); err != nil {
		log.Printf("transition observer failed: %v", err)
		os.Exit(4)
	}
}

func probeReadyFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("observation-ready marker is not a regular file")
	}
	return nil
}

func buildComponents(opts options, attempt attemptContract) (components, error) {
	source, err := edgemetrics.NewPowerSeries(attempt.powerSource, &http.Client{})
	if err != nil {
		return components{}, err
	}
	power, err := metricobserver.New(source)
	if err != nil {
		return components{}, err
	}
	config, err := kubeconfig.BuildConfig(kubeconfig.Options{})
	if err != nil {
		return components{}, err
	}
	if err := edgev1alpha1.AddToScheme(clientgoscheme.Scheme); err != nil {
		return components{}, err
	}
	reader, err := client.New(config, client.Options{Scheme: clientgoscheme.Scheme})
	if err != nil {
		return components{}, err
	}
	facts, err := transitionkube.New(reader, attempt.facts)
	if err != nil {
		return components{}, err
	}
	store, err := s3.New(s3.Config{
		LogicalStore: attempt.rawLocation.Store, Bucket: opts.storeBucket,
		Endpoint: opts.storeEndpoint, Region: opts.storeRegion,
		AccessKeyID: opts.accessKeyID, SecretAccessKey: opts.secretAccessKey,
		SessionToken: opts.sessionToken, TempDir: opts.tempDir,
	})
	if err != nil {
		return components{}, err
	}
	return components{power: power, powerRequest: attempt.powerRequest, facts: facts, store: store}, nil
}
