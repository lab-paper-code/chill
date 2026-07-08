package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterEnergyModelSpec defines the desired state of ClusterEnergyModel
type ClusterEnergyModelSpec struct{}

// ClusterEnergyModelStatus defines the observed state of ClusterEnergyModel
type ClusterEnergyModelStatus struct{}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// ClusterEnergyModel is the Schema for the clusterenergymodels API
type ClusterEnergyModel struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterEnergyModelSpec   `json:"spec,omitempty"`
	Status ClusterEnergyModelStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterEnergyModelList contains a list of ClusterEnergyModel
type ClusterEnergyModelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterEnergyModel `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterEnergyModel{}, &ClusterEnergyModelList{})
}
