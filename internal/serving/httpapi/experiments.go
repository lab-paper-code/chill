package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

const ExperimentSchemaVersion = "serving-experiment.v1alpha1"
const ExperimentPrefix = "serving/experiments.v1alpha1/"

// ExperimentSummary is the immutable publisher's summary, not a live evaluation.
// KPIs retain the publisher's populations, nulls, reasons and private limitations.
type ExperimentSummary struct {
	SchemaVersion string             `json:"schemaVersion"`
	ID            string             `json:"id"`
	From          time.Time          `json:"from"`
	To            time.Time          `json:"to"`
	Status        string             `json:"status"`
	Stages        []ExperimentStage  `json:"stages"`
	Sources       []ExperimentSource `json:"sources"`
	Definition    json.RawMessage    `json:"definition,omitempty"`
	Comparisons   json.RawMessage    `json:"comparisons,omitempty"`
	Facts         *ExperimentFacts   `json:"facts,omitempty"` // List-only projection; never published to the store.
	Outcome       json.RawMessage    `json:"outcome,omitempty"`
}

// ExperimentFacts projects only recorded definition fields before list responses
// omit the full definition. Missing facts stay absent, not inferred from live state.
type ExperimentFacts struct {
	Name        string                 `json:"name,omitempty"`
	Question    string                 `json:"question,omitempty"`
	Control     json.RawMessage        `json:"control,omitempty"`
	Stages      []ExperimentPolicyFact `json:"stages,omitempty"`
	SLOSources  []ExperimentSLOSource  `json:"sloSources,omitempty"`
	LoadSources []ExperimentLoadSource `json:"loadSources,omitempty"`
}

type ExperimentPolicyFact struct {
	ID              string          `json:"id"`
	Row             string          `json:"row,omitempty"`
	Mode            string          `json:"mode,omitempty"`
	Control         json.RawMessage `json:"control,omitempty"`
	LoadScenarioRef json.RawMessage `json:"loadScenarioRef,omitempty"`
}

type ExperimentSLOSource struct {
	Name       string          `json:"name"`
	UID        string          `json:"uid"`
	Generation json.RawMessage `json:"generation,omitempty"`
	SLO        json.RawMessage `json:"slo,omitempty"`
}

type ExperimentLoadSource struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

type ExperimentStage struct {
	ID   string          `json:"id"`
	From time.Time       `json:"from"`
	To   time.Time       `json:"to"`
	KPIs json.RawMessage `json:"kpis"`
}

type ExperimentSource struct {
	Store string `json:"store"`
	Key   string `json:"key"`
}

type ExperimentList struct {
	Items []ExperimentSummary `json:"items"`
}

// ValidExperimentID permits only one unescaped, non-traversing object-key element.
func ValidExperimentID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for i, c := range id {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' && i > 0 {
			continue
		}
		return false
	}
	return true
}

// ValidateExperimentSummary checks the immutable publication boundary shared by
// the publisher and reader. KPI values are preserved, never recalculated.
func ValidateExperimentSummary(summary ExperimentSummary, store, id string) error {
	if summary.SchemaVersion != ExperimentSchemaVersion || !ValidExperimentID(id) || summary.ID != id || strings.TrimSpace(summary.Status) == "" ||
		summary.From.IsZero() || !summary.To.After(summary.From) || summary.Stages == nil || summary.Sources == nil {
		return errors.New("invalid experiment summary schema, identity, interval or collections")
	}
	for _, field := range [...]struct {
		name  string
		value json.RawMessage
	}{{"definition", summary.Definition}, {"outcome", summary.Outcome}} {
		name, value := field.name, field.value
		if len(value) == 0 {
			continue
		}
		object := bytes.TrimSpace(value)
		if !json.Valid(value) || len(object) == 0 || object[0] != '{' {
			return fmt.Errorf("experiment %s must be a JSON object", name)
		}
	}
	for _, stage := range summary.Stages {
		if stage.ID == "" || stage.From.Before(summary.From) || stage.To.After(summary.To) || !stage.To.After(stage.From) ||
			len(stage.KPIs) == 0 || stage.KPIs[0] != '{' {
			return errors.New("invalid experiment stage interval or KPI object")
		}
	}
	for _, source := range summary.Sources {
		if source.Store != store || !experimentSourceKey(source.Key) {
			return errors.New("experiment source must reference a configured evidence store object")
		}
	}
	return nil
}

func experimentSourceKey(key string) bool {
	if key == "" || strings.TrimSpace(key) != key || strings.ContainsAny(key, "\\:%?#\x00\r\n\t") || strings.HasPrefix(key, "/") || path.Clean(key) != key {
		return false
	}
	for _, part := range strings.Split(key, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}

func (h *Handler) experiments(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	id := r.PathValue("id")
	if r.URL.RawPath != "" || id != "" && !ValidExperimentID(id) {
		sendError(w, &serving.Error{Code: "InvalidInput", Message: "experiment id must be one safe path element", Field: "id"})
		return
	}
	var value any
	var err error
	if id == "" && h.config.ListExperiments != nil {
		value, err = h.config.ListExperiments(r.Context())
	} else if id != "" && h.config.GetExperiment != nil {
		value, err = h.config.GetExperiment(r.Context(), id)
	} else {
		err = &serving.Error{Code: "ExperimentUnavailable", Message: "experiment summary service is unavailable"}
	}
	if err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, http.StatusOK, value)
}
