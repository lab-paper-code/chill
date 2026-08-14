package main

import (
	"errors"
	"fmt"
	"os"
	"sort"

	"sigs.k8s.io/yaml"

	"github.com/lab-paper-code/chill/internal/serving"
)

// staticRoutes answers every request from a table read once. Nothing revises it
// while the process runs, so it is a development stand-in that lets the entry
// point be deployed and reached before anything computes a weight.
//
// It lives here rather than beside the entry point because the entry point is
// written against entrypoint.Routes and takes no interest in where weights come
// from. Something that computes them replaces this file alone.
type staticRoutes struct {
	Models map[string]serving.Weights `json:"models"`
}

func (s staticRoutes) WeightsFor(model string) (serving.Weights, bool) {
	weights, served := s.Models[model]
	return weights, served
}

func (s staticRoutes) modelNames() []string {
	models := make([]string, 0, len(s.Models))
	for model := range s.Models {
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

func (s staticRoutes) validate() error {
	if len(s.Models) == 0 {
		return errors.New("the table serves no model")
	}
	for model, weights := range s.Models {
		if model == "" {
			return errors.New("the table names a model with an empty name")
		}
		if err := weights.Validate(); err != nil {
			return fmt.Errorf("model %q: %w", model, err)
		}
	}
	return nil
}

// readRoutes reads a routing table, in which each model names the nodes serving
// it and the weight each one carries:
//
//	models:
//	  mobilenet:
//	    - node: jetsono
//	      service: chill-serving-jetsono
//	      weight: 1
//
// A table nothing could be served from is rejected here rather than at the
// first request.
func readRoutes(path string) (staticRoutes, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return staticRoutes{}, fmt.Errorf("read the routing table: %w", err)
	}
	var routes staticRoutes
	if err := yaml.UnmarshalStrict(raw, &routes); err != nil {
		return staticRoutes{}, fmt.Errorf("parse the routing table: %w", err)
	}
	if err := routes.validate(); err != nil {
		return staticRoutes{}, fmt.Errorf("validate the routing table: %w", err)
	}
	return routes, nil
}
