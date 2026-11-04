package main

import (
	"bytes"
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
	result, err := s.listSummaries(ctx)
	for i := range result.Items {
		summary := &result.Items[i]
		var definition struct {
			Run struct {
				Name string `json:"name"`
			} `json:"run"`
			Spec struct {
				Question      string          `json:"question"`
				SettingsPatch json.RawMessage `json:"settingsPatch"`
				Stages        []struct {
					httpapi.ExperimentPolicyFact
					SettingsPatch json.RawMessage `json:"settingsPatch"`
				} `json:"stages"`
				Comparisons json.RawMessage `json:"comparisons"`
			} `json:"spec"`
			Workloads     []httpapi.ExperimentSLOSource  `json:"workloads"`
			LoadScenarios []httpapi.ExperimentLoadSource `json:"loadScenarios"`
		}
		// Project the immutable publication, never today's settings or objects.
		if json.Unmarshal(summary.Definition, &definition) == nil {
			if definition.Run.Name != "" || definition.Spec.Question != "" || len(definition.Spec.SettingsPatch) != 0 ||
				len(definition.Spec.Stages) != 0 || len(definition.Workloads) != 0 || len(definition.LoadScenarios) != 0 {
				stages := make([]httpapi.ExperimentPolicyFact, len(definition.Spec.Stages))
				for i, stage := range definition.Spec.Stages {
					patch := definition.Spec.SettingsPatch
					if raw := bytes.TrimSpace(stage.SettingsPatch); len(raw) != 0 && !bytes.Equal(raw, []byte("null")) {
						patch = raw
					}
					stages[i] = stage.ExperimentPolicyFact
					stages[i].SettingsPatchKeys = experimentSettingsPatchKeys(patch)
				}
				summary.Facts = &httpapi.ExperimentFacts{
					Name: definition.Run.Name, Question: definition.Spec.Question,
					Stages: stages, SLOSources: definition.Workloads, LoadSources: definition.LoadScenarios,
				}
			}
		}
		summary.Comparisons = definition.Spec.Comparisons
		summary.Definition = nil
	}
	return result, err
}

// experimentSettingsPatchKeys lists recorded leaf paths, not live settings or
// inferred effects. An invalid patch is unknown; no declared patch is empty.
func experimentSettingsPatchKeys(raw json.RawMessage) []string {
	keys := []string{}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return keys
	}
	var patch map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&patch) != nil || patch == nil {
		return nil
	}
	var visit func(map[string]any, string)
	visit = func(object map[string]any, prefix string) {
		for key, value := range object {
			path := prefix + key
			if child, ok := value.(map[string]any); ok && len(child) != 0 {
				visit(child, path+".")
			} else {
				keys = append(keys, path)
			}
		}
	}
	visit(patch, "")
	sort.Strings(keys)
	return keys
}

// errUnreadableSummary marks a stored summary whose own content or identity is unusable,
// as opposed to a store, transport or cancellation failure while reading it.
var errUnreadableSummary = errors.New("unreadable experiment summary")

// listSummaries keeps definitions for request-local consumers such as templates.
// Each call lists and reads the store afresh; there is no cross-request cache.
// One unreadable summary is reported and skipped; store failures still fail the list.
func (s *experimentService) listSummaries(ctx context.Context) (httpapi.ExperimentList, error) {
	result := httpapi.ExperimentList{Items: []httpapi.ExperimentSummary{}, Unreadable: []httpapi.ExperimentUnreadable{}}
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
		var closeErr closeFailure
		if errors.Is(err, errUnreadableSummary) && !errors.As(err, &closeErr) {
			result.Unreadable = append(result.Unreadable, httpapi.ExperimentUnreadable{ID: id, Reason: err.Error()})
			continue
		}
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
		return httpapi.ExperimentSummary{}, &serving.Error{
			Code: "InvalidInput", Message: "experiment id must be one safe path element", Field: "id",
		}
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

func (s *experimentService) read(
	ctx context.Context, object evidencestore.ObjectIdentity, id string,
) (summary httpapi.ExperimentSummary, err error) {
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	if object.Store != s.store || object.Validate() != nil || s.maxBytes <= 0 || object.Size > s.maxBytes {
		return summary, fmt.Errorf("%w: invalid experiment summary object identity or size", errUnreadableSummary)
	}
	body, err := s.reader.Open(ctx, object)
	if err != nil {
		return summary, err
	}
	defer func() {
		if closeErr := body.Close(); closeErr != nil {
			err = errors.Join(err, closeFailure{closeErr})
		}
	}()
	stream := &streamErrors{Reader: body}
	limited := &io.LimitedReader{R: stream, N: s.maxBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&summary); err != nil {
		return summary, contentDefect(stream, ctx, fmt.Errorf("decode experiment summary: %w", err))
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return summary, contentDefect(stream, ctx,
			fmt.Errorf("experiment summary must contain one bounded JSON object: %v", err))
	}
	if limited.N == 0 {
		return summary, contentDefect(stream, ctx, errors.New("experiment summary exceeds configured byte limit"))
	}
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	if err := httpapi.ValidateExperimentSummary(summary, s.store, id); err != nil {
		return summary, fmt.Errorf("%w: %w", errUnreadableSummary, err)
	}
	return summary, nil
}

// closeFailure keeps a store-side Close error fatal even when the content was also defective.
type closeFailure struct{ error }

func (c closeFailure) Unwrap() error { return c.error }

// streamErrors records the body's own read failures (transport, timeout, cancellation).
type streamErrors struct {
	io.Reader
	err error
}

func (s *streamErrors) Read(p []byte) (int, error) {
	n, err := s.Reader.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		s.err = err
	}
	return n, err
}

// contentDefect marks a decode failure as unreadable content only when the stream itself
// did not fail and the request is still live; otherwise the stream error stays fatal.
func contentDefect(stream *streamErrors, ctx context.Context, err error) error {
	if stream.err != nil {
		return stream.err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return fmt.Errorf("%w: %w", errUnreadableSummary, err)
}
