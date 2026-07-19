package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/lab-paper-code/chill/internal/operatorconsole"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const maximumEvidencePassportBytes int64 = 4 << 20

type evidencePassportQuery interface {
	Read(context.Context) (operatorconsole.EvidencePassport, error)
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
		service: opts.consoleService, run: opts.name,
	}, nil
}

func (q *serviceProxyEvidencePassportQuery) Read(ctx context.Context) (operatorconsole.EvidencePassport, error) {
	stream, err := q.restClient.Get().
		Namespace(q.namespace).
		Resource("services").
		Name(q.service+":http").
		SubResource("proxy").
		Suffix("api", "v1", "namespaces", q.namespace, "profiling-runs", q.run, "evidence-passport").
		Stream(ctx)
	if err != nil {
		return operatorconsole.EvidencePassport{}, fmt.Errorf("read Evidence Passport through Console Service: %w", err)
	}
	payload, readErr := io.ReadAll(io.LimitReader(stream, maximumEvidencePassportBytes+1))
	closeErr := stream.Close()
	if readErr != nil {
		return operatorconsole.EvidencePassport{}, fmt.Errorf("read Console response: %w", readErr)
	}
	if closeErr != nil {
		return operatorconsole.EvidencePassport{}, fmt.Errorf("close Console response: %w", closeErr)
	}
	if int64(len(payload)) > maximumEvidencePassportBytes {
		return operatorconsole.EvidencePassport{}, errors.New("Console response exceeds the Evidence Passport size limit")
	}
	var passport operatorconsole.EvidencePassport
	if err := json.Unmarshal(payload, &passport); err != nil {
		return operatorconsole.EvidencePassport{}, fmt.Errorf("decode Console Evidence Passport: %w", err)
	}
	if passport.APIVersion != operatorconsole.EvidencePassportAPIVersion || passport.Kind != "EvidencePassport" ||
		passport.Run.Namespace != q.namespace || passport.Run.Name != q.run {
		return operatorconsole.EvidencePassport{}, errors.New("Console returned an unexpected Evidence Passport identity")
	}
	return passport, nil
}
