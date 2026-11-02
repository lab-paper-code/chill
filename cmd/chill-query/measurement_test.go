package main

import (
	"context"
	"encoding/json"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	batch "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func measurementScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := batch.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func (o *measurementService) profilePayload(ctx context.Context, view httpapi.ProfilesView) (json.RawMessage, error) {
	response, err := o.buildProfiles(ctx, view)
	if err != nil {
		return nil, err
	}
	return json.Marshal(response)
}
