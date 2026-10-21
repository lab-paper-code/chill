// serving-operation sends an operation measurement sequence or a manual
// prepare/remove to ChillServer and prints ChillServer's result. ChillServer
// admits the Node, runs the operations and stores the evidence.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"k8s.io/client-go/rest"
)

const (
	measurementsPath = "/api/v1/measurements/operations"
	operationsPath   = "/api/v1/scheduler/operations"
	maxManifestBytes = 4 << 20
	maxResultBytes   = 64 << 20
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("serving-operation", flag.ContinueOnError)
	var measurementFile, executeFile, server, caFile, tokenFile string
	var timeout time.Duration
	flags.StringVar(&measurementFile, "measurement-config", "", "JSON array of operation measurement requests on one Node")
	flags.StringVar(&executeFile, "execute-config", "",
		"JSON manual prepare or remove of one Workload outside autonomous control")
	flags.StringVar(&server, "chill-server", "", "ChillServer HTTPS origin")
	flags.StringVar(&caFile, "chill-ca-file", "", "ChillServer CA certificate path")
	flags.StringVar(&tokenFile, "chill-token-file", "", "caller bearer token path")
	flags.DurationVar(&timeout, "timeout", 30*time.Minute, "bound of the whole request")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if (measurementFile == "") == (executeFile == "") || timeout <= 0 {
		return errors.New("exactly one of measurement-config or execute-config and a positive timeout are required")
	}
	c, err := chillClient(server, caFile, tokenFile)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if measurementFile != "" {
		var requests []servingkube.MeasurementRequest
		if err = readManifest(measurementFile, &requests); err != nil {
			return err
		}
		var result servingkube.SequenceResult
		if err = post(ctx, c, server, measurementsPath, requests, &result, out); err != nil {
			return err
		}
		if result.Error != "" {
			return errors.New(result.Error)
		}
		return nil
	}
	var operation servingkube.ManualOperation
	if err = readManifest(executeFile, &operation); err != nil {
		return err
	}
	var result servingkube.OperationResult
	if err = post(ctx, c, server, operationsPath, operation, &result, out); err != nil {
		return err
	}
	if result.Error != "" {
		return errors.New(result.Error)
	}
	return nil
}

// chillClient trusts only the given CA, sends only the given token and never
// follows a redirect.
func chillClient(origin, caFile, tokenFile string) (*http.Client, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("chill-server must be one HTTPS origin without credentials, path, query or fragment")
	}
	if caFile == "" || tokenFile == "" {
		return nil, errors.New("explicit chill-ca-file and chill-token-file are required")
	}
	c, err := rest.HTTPClientFor(&rest.Config{
		Host: origin, BearerTokenFile: tokenFile, TLSClientConfig: rest.TLSClientConfig{CAFile: caFile},
	})
	if err != nil {
		return nil, err
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c, nil
}

// readManifest strictly decodes one bounded JSON value.
func readManifest(path string, target any) error {
	stream, err := os.Open(path)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(stream, maxManifestBytes+1))
	err = errors.Join(err, stream.Close())
	if err != nil {
		return err
	}
	if len(raw) > maxManifestBytes {
		return errors.New("operation manifest exceeds bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return err
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("operation manifest has trailing JSON")
	}
	return nil
}

// post sends body, prints the bounded response and decodes it into result.
func post(ctx context.Context, c *http.Client, origin, path string, body, result any, out io.Writer) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	target := strings.TrimSuffix(origin, "/") + path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.Do(request)
	if err != nil {
		return err
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResultBytes+1))
	err = errors.Join(err, response.Body.Close())
	if err != nil {
		return err
	}
	if len(payload) > maxResultBytes {
		return errors.New("ChillServer result exceeds bound")
	}
	if _, err = out.Write(payload); err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("ChillServer %s: HTTP %d", path, response.StatusCode)
	}
	return json.Unmarshal(payload, result)
}
