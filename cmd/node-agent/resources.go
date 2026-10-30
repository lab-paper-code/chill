package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type resourceAgentConfig struct {
	URL              string `json:"url"`
	CAFile           string `json:"caFile"`
	TokenFile        string `json:"tokenFile"`
	SampleInterval   string `json:"sampleInterval"`
	BatchInterval    string `json:"batchInterval"`
	RequestTimeout   string `json:"requestTimeout"`
	MaxResponseBytes int64  `json:"maxResponseBytes"`
}

func runResourceAgent(ctx context.Context, kube kubernetes.Interface, opts options) error {
	raw := []byte(opts.resourcesConfig)
	var config resourceAgentConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return err
	}
	endpoint, err := url.Parse(config.URL)
	if err != nil {
		return err
	}
	if endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" ||
		endpoint.Path != "/chill/api/v1/internal/observations/resources" {
		return fmt.Errorf("resources URL must be the fixed internal HTTPS endpoint")
	}
	sampleInterval, err := time.ParseDuration(config.SampleInterval)
	if err != nil {
		return err
	}
	batchInterval, err := time.ParseDuration(config.BatchInterval)
	if err != nil {
		return err
	}
	timeout, err := time.ParseDuration(config.RequestTimeout)
	if err != nil {
		return err
	}
	if sampleInterval <= 0 || batchInterval < sampleInterval || timeout <= 0 ||
		config.MaxResponseBytes <= 0 || config.TokenFile == "" {
		return fmt.Errorf("positive resources intervals, timeout and response bound are required")
	}
	ca, err := os.ReadFile(config.CAFile)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return fmt.Errorf("resources CA has no certificates")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer transport.CloseIdleConnections()
	node, err := kube.CoreV1().Nodes().Get(ctx, opts.nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	identity := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	send := func(samples []serving.ResourceSample) ([]serving.ResourceTarget, error) {
		return sendResourceBatch(ctx, client, config, serving.ResourceBatch{Node: identity, Samples: samples})
	}
	targets, err := send([]serving.ResourceSample{})
	if err != nil {
		log.Printf("resource handshake failed: %v", err)
	}
	samples := make([]serving.ResourceSample, 0, int(batchInterval/sampleInterval)+1)
	sampleTick, batchTick := time.NewTicker(sampleInterval), time.NewTicker(batchInterval)
	defer sampleTick.Stop()
	defer batchTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case at := <-sampleTick.C:
			if targets != nil {
				samples = append(samples, SampleResources(opts.hostRoot, targets, at.UTC()))
			}
		case <-batchTick.C:
			next, sendErr := send(samples)
			samples = samples[:0] // Failed or delivered: never replay a batch.
			if sendErr != nil {
				targets = nil
				log.Printf("resource batch dropped: %v", sendErr)
			} else {
				targets = next
			}
		}
	}
}
func sendResourceBatch(
	ctx context.Context, client *http.Client, config resourceAgentConfig, batch serving.ResourceBatch,
) ([]serving.ResourceTarget, error) {
	if batch.Samples == nil {
		batch.Samples = []serving.ResourceSample{}
	}
	body, err := json.Marshal(batch)
	if err != nil {
		return nil, err
	}
	token, err := os.ReadFile(config.TokenFile)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("resource POST: %s", response.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, config.MaxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > config.MaxResponseBytes {
		return nil, fmt.Errorf("resources reply exceeds bound")
	}
	var reply struct {
		Targets []serving.ResourceTarget `json:"targets"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, err
	}
	if reply.Targets == nil {
		reply.Targets = []serving.ResourceTarget{}
	}
	return reply.Targets, nil
}
