package application

import (
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
)

// ProfilesView is the complete catalog snapshot published by configuration acceptance.
type ProfilesView struct {
	ConfigVersion string `json:"configVersion"`
	servingkube.CatalogView
	Preparations     map[string]servingstate.PrepareSpec `json:"preparations"`
	Occupancy        []servingkube.OccupancySelection    `json:"-"`
	OccupancySkipped []servingkube.OccupancySkipped      `json:"-"`
}
