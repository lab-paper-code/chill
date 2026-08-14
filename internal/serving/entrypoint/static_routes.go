package entrypoint

import (
	"errors"
	"fmt"

	"sigs.k8s.io/yaml"

	"github.com/lab-paper-code/chill/internal/serving"
)

// StaticRoutes answers every request from the same table. Nothing revises it
// while the process runs, so it is a development stand-in that lets the entry
// point be deployed and reached before anything computes weights. Replacing it
// is supplying another Routes implementation to the same Handler.
type StaticRoutes struct {
	Models map[string]serving.Weights `json:"models"`
}

// WeightsFor implements Routes.
func (s StaticRoutes) WeightsFor(model string) (serving.Weights, bool) {
	weights, served := s.Models[model]
	return weights, served
}

// Validate rejects a table that could not distribute a request, so a table that
// would refuse every consumer is caught when it is read rather than when the
// first request arrives.
func (s StaticRoutes) Validate() error {
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

// ParseStaticRoutes reads a routing table, in which each model names the nodes
// serving it and the weight each one carries:
//
//	models:
//	  mobilenet:
//	    - node: jetsono
//	      service: chill-serving-jetsono
//	      weight: 1
func ParseStaticRoutes(raw []byte) (StaticRoutes, error) {
	var routes StaticRoutes
	if err := yaml.UnmarshalStrict(raw, &routes); err != nil {
		return StaticRoutes{}, fmt.Errorf("parse the routing table: %w", err)
	}
	if err := routes.Validate(); err != nil {
		return StaticRoutes{}, fmt.Errorf("validate the routing table: %w", err)
	}
	return routes, nil
}
