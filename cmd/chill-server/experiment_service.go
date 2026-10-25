package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
)

// experimentService reads only final immutable summaries from the existing store.
type experimentService struct {
	reader   evidencestore.Reader
	lister   evidencestore.Lister
	store    string
	maxBytes int64
}

func (s *experimentService) list(ctx context.Context) (httpapi.ExperimentList, error) {
	result := httpapi.ExperimentList{Items: []httpapi.ExperimentSummary{}}
	objects, err := s.lister.List(ctx, httpapi.ExperimentPrefix)
	if err != nil {
		return result, err
	}
	for _, object := range objects {
		id, ok := experimentObjectID(object.Key)
		if !ok {
			continue
		}
		summary, err := s.read(ctx, object, id)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, summary)
	}
	sort.Slice(result.Items, func(i, j int) bool {
		a, b := result.Items[i], result.Items[j]
		if a.From.Equal(b.From) {
			return a.ID < b.ID
		}
		return a.From.After(b.From)
	})
	return result, nil
}

func (s *experimentService) get(ctx context.Context, id string) (httpapi.ExperimentSummary, error) {
	if !httpapi.ValidExperimentID(id) {
		return httpapi.ExperimentSummary{}, &serving.Error{Code: "InvalidInput", Message: "experiment id must be one safe path element", Field: "id"}
	}
	key := httpapi.ExperimentPrefix + id + "/summary.json"
	objects, err := s.lister.List(ctx, key)
	if err != nil {
		return httpapi.ExperimentSummary{}, err
	}
	for _, object := range objects {
		if object.Key != key {
			continue
		}
		summary, err := s.read(ctx, object, id)
		if errors.Is(err, evidencestore.ErrNotFound) {
			return httpapi.ExperimentSummary{}, experimentNotFound()
		}
		return summary, err
	}
	return httpapi.ExperimentSummary{}, experimentNotFound()
}

func experimentNotFound() error {
	return &serving.Error{Code: "NotFound", Message: "experiment summary not found"}
}

func experimentObjectID(key string) (string, bool) {
	id, ok := strings.CutPrefix(key, httpapi.ExperimentPrefix)
	if !ok {
		return "", false
	}
	id, ok = strings.CutSuffix(id, "/summary.json")
	return id, ok && httpapi.ValidExperimentID(id)
}

func (s *experimentService) read(ctx context.Context, object evidencestore.ObjectIdentity, id string) (summary httpapi.ExperimentSummary, err error) {
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	if object.Store != s.store || object.Validate() != nil || s.maxBytes <= 0 || object.Size > s.maxBytes {
		return summary, errors.New("invalid experiment summary object identity or size")
	}
	body, err := s.reader.Open(ctx, object)
	if err != nil {
		return summary, err
	}
	defer func() { err = errors.Join(err, body.Close()) }()
	limited := &io.LimitedReader{R: body, N: s.maxBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&summary); err != nil {
		return summary, fmt.Errorf("decode experiment summary: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return summary, errors.New("experiment summary must contain one bounded JSON object")
	}
	if limited.N == 0 {
		return summary, errors.New("experiment summary exceeds configured byte limit")
	}
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	if err := httpapi.ValidateExperimentSummary(summary, s.store, id); err != nil {
		return summary, err
	}
	return summary, nil
}

