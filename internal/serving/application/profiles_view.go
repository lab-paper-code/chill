package application

import servingkube "github.com/lab-paper-code/chill/internal/serving/kube"

// ProfilesView is the complete catalog snapshot published by configuration acceptance.
type ProfilesView struct {
	ConfigVersion string `json:"configVersion"`
	servingkube.CatalogView
	Preparations map[string]servingkube.PrepareSpec `json:"preparations"`
	Occupancy    []servingkube.OccupancySelection   `json:"-"`
}
