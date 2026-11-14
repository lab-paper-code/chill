package application

import (
	"context"
	"slices"
	"testing"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
)

func TestPreparationRebuildsChangedProfileEvidence(t *testing.T) {
	fixture := newPreparationCatalogFixture(t)
	if err := fixture.service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	previous := fixture.service.Profiles.Load().Preparations[fixture.profile.ID]
	changed := fixture.profile
	changed.Reference.Key += ".replacement"
	changed.SourceVersion += ".replacement"
	changed.Batches = slices.Clone(changed.Batches)
	for i := range changed.Batches {
		batch := &changed.Batches[i]
		batch.ServiceSamples = native.CloneServiceSamples(batch.ServiceSamples)
		for j := range batch.ServiceSamples {
			batch.ServiceSamples[j].ServiceDurationNanos *= 2
		}
		var err error
		batch.ServiceDistribution, err = native.NewServiceDistribution(batch.ServiceSamples)
		if err != nil {
			t.Fatal(err)
		}
	}
	next, err := fixture.service.preparation(context.Background(), *fixture.service.configuration.Load(), changed)
	if err != nil {
		t.Fatal(err)
	}
	if next.Bundle == previous.Bundle {
		t.Fatal("changed measurement reuses the previous native profile bundle")
	}
}
