package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeviceProfileSpec defines the desired state of DeviceProfile
type DeviceProfileSpec struct{}

// DeviceProfileStatus defines the observed state of DeviceProfile
type DeviceProfileStatus struct{}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// DeviceProfile is the Schema for the deviceprofiles API
type DeviceProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DeviceProfileSpec   `json:"spec,omitempty"`
	Status DeviceProfileStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DeviceProfileList contains a list of DeviceProfile
type DeviceProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DeviceProfile{}, &DeviceProfileList{})
}
