package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/lab-paper-code/chill/internal/queryapi"
)

const maximumEvidencePassportBytes int64 = 4 << 20

type evidencePassportQuery interface {
	Read(context.Context) (queryapi.EvidencePassport, error)
}

type ownerEvidencePassportQuery struct{ *ownerClient }

func newEvidencePassportQuery(opts operationOptions) (evidencePassportQuery, error) {
	config, defaultNamespace, err := loadKubernetesConfig(opts.kubeconfig, opts.context)
	if err != nil {
		return nil, err
	}
	if opts.namespace == "" {
		opts.namespace = defaultNamespace
	}
	owner, err := newOwnerHTTPSClient(opts, config)
	if err != nil {
		return nil, err
	}
	return &ownerEvidencePassportQuery{owner}, nil
}

func (q *ownerEvidencePassportQuery) Read(ctx context.Context) (queryapi.EvidencePassport, error) {
	var passport queryapi.EvidencePassport
	path := "/api/v1/namespaces/" + url.PathEscape(q.opts.namespace) + "/profiling-runs/" +
		url.PathEscape(q.opts.name) + "/evidence-passport"
	if err := q.read(ctx, http.MethodGet, path, nil, "", maximumEvidencePassportBytes, &passport); err != nil {
		return passport, err
	}
	if passport.APIVersion != queryapi.EvidencePassportAPIVersion || passport.Kind != "EvidencePassport" ||
		passport.Run.Namespace != q.opts.namespace || passport.Run.Name != q.opts.name {
		return passport, errors.New("owner returned an unexpected Evidence Passport identity")
	}
	return passport, nil
}
