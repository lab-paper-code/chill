// chill-router is the standalone inference data plane. It has no controller or
// observer loops and never sends inference bodies through control.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"github.com/lab-paper-code/chill/internal/kubeconfig"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving/gateway"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"k8s.io/client-go/kubernetes"
)

type options struct {
	bind, cert, key, control, ca, token, podUID, buffer string
	kube                                                kubeconfig.Options
	store                                               stores3.Config
	queue                                               int
	maxBody                                             int64
	requestTimeout, drainTimeout, retention             time.Duration
}

func (o *options) flags(f *flag.FlagSet) {
	f.StringVar(&o.bind, "bind-address", ":8080", "HTTPS listen address.")
	f.StringVar(&o.cert, "tls-cert", "", "Absolute server certificate file.")
	f.StringVar(&o.key, "tls-key", "", "Absolute server private key file.")
	f.StringVar(&o.control, "control-url", "", "Fixed authenticated wss control origin, without path.")
	f.StringVar(&o.ca, "control-ca-file", "", "Absolute CA file for control hostname verification.")
	f.StringVar(&o.token, "control-token-file", "", "Absolute rotating bearer token file for internal channel.")
	f.StringVar(&o.podUID, "pod-uid", "", "Router Pod UID; a random process generation is appended.")
	f.StringVar(&o.buffer, "observation-directory", "", "Absolute persistent private router buffer root; previous generations are retained.")
	f.StringVar(&o.kube.APIServer, "kube-api-server", "", "Explicit Kubernetes review API endpoint; otherwise in-cluster.")
	f.StringVar(&o.kube.CAFile, "kube-ca-file", "", "Kubernetes review API CA file.")
	f.StringVar(&o.kube.TokenFile, "kube-token-file", "", "Kubernetes review API service account token file.")
	f.StringVar(&o.store.LogicalStore, "store-name", "", "Logical Evidence Store name.")
	f.StringVar(&o.store.Endpoint, "store-endpoint", "", "S3-compatible Evidence Store endpoint.")
	f.StringVar(&o.store.Bucket, "store-bucket", "", "Evidence Store bucket.")
	f.StringVar(&o.store.Region, "store-region", "", "Evidence Store region.")
	f.IntVar(&o.queue, "queue-limit", 1024, "Per-workload ingress waiting bound.")
	f.Int64Var(&o.maxBody, "max-request-bytes", 8<<20, "Bounded raw inference request bytes.")
	f.DurationVar(&o.requestTimeout, "request-timeout", 30*time.Second, "HTTP boundary and final delivery bound.")
	f.DurationVar(&o.drainTimeout, "drain-timeout", 2*time.Minute, "Shutdown physical drain bound; missing completion stays unresolved.")
	f.DurationVar(&o.retention, "observation-retention", 2*time.Hour, "Positive local retention after immutable archive upload.")
}
func (o options) validate() error {
	for _, p := range []string{o.cert, o.key, o.ca, o.token, o.buffer} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return errors.New("TLS, token and buffer paths must be clean absolute paths")
		}
	}
	if o.bind == "" || o.control == "" || o.podUID == "" || o.queue < 1 || o.maxBody < 1 || o.requestTimeout <= 0 || o.requestTimeout > 129*time.Second || o.drainTimeout <= 0 || o.retention <= 0 {
		return errors.New("control origin, Pod UID and positive bounded router configuration required")
	}
	for _, c := range o.podUID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return errors.New("Pod UID must use letters, digits, hyphens or underscores")
		}
	}
	return nil
}
func main() {
	var o options
	o.flags(flag.CommandLine)
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if err := run(ctx, o); err != nil {
		slog.Error("router stopped", "error", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, o options) (result error) {
	if err := o.validate(); err != nil {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	routerID := o.podUID + "-" + hex.EncodeToString(random[:])
	o.store.AccessKeyID = os.Getenv("AWS_ACCESS_KEY_ID")
	o.store.SecretAccessKey = os.Getenv("AWS_SECRET_ACCESS_KEY")
	o.store.SessionToken = os.Getenv("AWS_SESSION_TOKEN")
	objects, err := stores3.New(o.store)
	if err != nil {
		return err
	}
	config, err := kubeconfig.BuildConfig(o.kube)
	if err != nil {
		return err
	}
	config.QPS = 50
	config.Burst = 100
	reviews, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	// Per-Pod ownership allows N routers to share a visible buffer root without
	// sharing writable stream files. It also fences the old process before custody.
	owner, err := serving.AcquireObservationOwnership(filepath.Join(o.buffer, ".owners", o.podUID), func(msg string, args ...any) { slog.Info(msg, args...) })
	if err != nil {
		return err
	}
	defer owner.Close()
	directory := filepath.Join(o.buffer, routerID)
	log, err := serving.NewObservationLog(serving.ObservationLogConfig{Directory: directory, RouterID: routerID, Store: o.store.LogicalStore, Writer: objects, Retention: o.retention})
	if err != nil {
		return err
	}
	logs := []*serving.ObservationLog{log}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), o.drainTimeout)
		defer cancel()
		for _, source := range logs {
			result = errors.Join(result, source.Close(closeCtx))
		}
	}()
	backendCtx, cancelBackend := context.WithCancel(context.Background())
	defer cancelBackend()
	var recovery []gateway.Recovery
	// Recover each preserved generation with the same archive writer. Its assigned
	// rows continue protecting incomplete hours; no completion is synthesized.
	entries, err := os.ReadDir(o.buffer)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == routerID || !strings.HasPrefix(entry.Name(), o.podUID+"-") {
			continue
		}
		prior, err := serving.NewObservationLog(serving.ObservationLogConfig{Directory: filepath.Join(o.buffer, entry.Name()), RouterID: entry.Name(), Store: o.store.LogicalStore, Writer: objects, Retention: o.retention})
		if err != nil {
			return err
		}
		logs = append(logs, prior)
		restored, err := serving.NewRuntime(backendCtx, serving.RuntimeConfig{QueueLimit: o.queue, ObservationLog: prior, RouterID: entry.Name()})
		if err != nil {
			return err
		}
		restored.SetGatewayConnected(false)
		recovery = append(recovery, gateway.Recovery{RouterID: entry.Name(), Runtime: restored})
	}
	runtime, err := serving.NewRuntime(backendCtx, serving.RuntimeConfig{QueueLimit: o.queue, ObservationLog: log, RouterID: routerID})
	if err != nil {
		return err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 0
	transport.MaxIdleConnsPerHost = 256
	transport.IdleConnTimeout = 30 * time.Second
	defer transport.CloseIdleConnections()
	router, err := gateway.NewRouter(gateway.RouterConfig{ControlURL: o.control, CAFile: o.ca, TokenFile: o.token, RouterID: routerID, Runtime: runtime, Recovery: recovery, Native: &firstfloor.Client{HTTPClient: &http.Client{Transport: transport}}})
	if err != nil {
		return err
	}
	handler, err := httpapi.NewRouterHandler(runtime, &servingkube.Authorizer{Client: reviews}, log, httpapi.HandlerConfig{MaxRequestBytes: o.maxBody, RequestTimeout: o.requestTimeout})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", o.bind)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: o.requestTimeout, IdleTimeout: o.requestTimeout}
	failures := make(chan error, 1)
	go func() { failures <- server.ServeTLS(listener, o.cert, o.key) }()
	channelCtx, cancelChannel := context.WithCancel(context.Background())
	defer cancelChannel()
	go router.Run(channelCtx)
	// One independent archive writer reuses the existing immutable uploader. Old
	// local generation buffers are reopened and flushed without truncating them.
	archiveCtx, cancelArchive := context.WithCancel(context.Background())
	defer cancelArchive()
	archiveDone := make(chan struct{})
	go func() {
		defer close(archiveDone)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-archiveCtx.Done():
				return
			case <-ticker.C:
				for _, source := range logs {
					if err := source.Flush(archiveCtx); err != nil {
						slog.Error("archive router observations", "routerID", routerID, "error", err)
					}
				}
			}
		}
	}()
	slog.Info("router listening", "routerID", routerID, "pid", os.Getpid(), "address", listener.Addr().String())
	select {
	case <-ctx.Done():
	case err = <-failures:
		if !errors.Is(err, http.ErrServerClosed) {
			result = fmt.Errorf("router HTTPS: %w", err)
		}
	}
	runtime.SetGatewayConnected(false)
	shutdown, cancel := context.WithTimeout(context.Background(), o.drainTimeout)
	defer cancel()
	// Close runtime admission permanently before waiting on HTTP delivery. The
	// control channel remains alive for real native completion during the drain.
	result = errors.Join(result, runtime.Shutdown(shutdown), server.Shutdown(shutdown))
	for _, old := range recovery {
		result = errors.Join(result, old.Runtime.Shutdown(shutdown))
	}
	cancelChannel()
	cancelArchive()
	<-archiveDone
	return result
}
