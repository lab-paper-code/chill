package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/lab-paper-code/chill/internal/queryapi"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const maximumEvidencePassportBytes int64 = 4 << 20

type evidencePassportQuery interface {
	Read(context.Context) (queryapi.EvidencePassport, error)
}

type serviceProxyEvidencePassportQuery struct {
	restClient rest.Interface
	namespace  string
	service    string
	run        string
}

func newEvidencePassportQuery(opts profilingRunShowOptions) (evidencePassportQuery, error) {
	config, defaultNamespace, err := loadKubernetesConfig(opts.kubeconfig, opts.context)
	if err != nil {
		return nil, err
	}
	namespace := opts.namespace
	if namespace == "" {
		namespace = defaultNamespace
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("construct Kubernetes Service proxy client: %w", err)
	}
	return &serviceProxyEvidencePassportQuery{
		restClient: clientset.CoreV1().RESTClient(), namespace: namespace,
		service: opts.queryService, run: opts.name,
	}, nil
}

func (q *serviceProxyEvidencePassportQuery) Read(ctx context.Context) (queryapi.EvidencePassport, error) {
	stream, err := q.restClient.Get().
		Namespace(q.namespace).
		Resource("services").
		Name(q.service+":http").
		SubResource("proxy").
		Suffix("api", "v1", "namespaces", q.namespace, "profiling-runs", q.run, "evidence-passport").
		Stream(ctx)
	if err != nil {
		return queryapi.EvidencePassport{}, fmt.Errorf("read Evidence Passport through Query API Service: %w", err)
	}
	payload, readErr := io.ReadAll(io.LimitReader(stream, maximumEvidencePassportBytes+1))
	closeErr := stream.Close()
	if readErr != nil {
		return queryapi.EvidencePassport{}, fmt.Errorf("read Query API response: %w", readErr)
	}
	if closeErr != nil {
		return queryapi.EvidencePassport{}, fmt.Errorf("close Query API response: %w", closeErr)
	}
	if int64(len(payload)) > maximumEvidencePassportBytes {
		return queryapi.EvidencePassport{}, errors.New("Query API response exceeds the Evidence Passport size limit")
	}
	var passport queryapi.EvidencePassport
	if err := json.Unmarshal(payload, &passport); err != nil {
		return queryapi.EvidencePassport{}, fmt.Errorf("decode Query API Evidence Passport: %w", err)
	}
	if passport.APIVersion != queryapi.EvidencePassportAPIVersion || passport.Kind != "EvidencePassport" ||
		passport.Run.Namespace != q.namespace || passport.Run.Name != q.run {
		return queryapi.EvidencePassport{}, errors.New("Query API returned an unexpected Evidence Passport identity")
	}
	return passport, nil
}
