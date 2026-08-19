package operatorconsole

import (
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/deviceclass"
)

// InventorySnapshot is one read-through projection from three authoritative
// Kubernetes LIST responses. Their resource versions are kept separately;
// this type does not claim an atomic cross-resource snapshot.
type InventorySnapshot struct {
	ReadAt                       time.Time
	DeviceClassesResourceVersion string
	NodesResourceVersion         string
	ModelSpecsResourceVersion    string
	Classes                      []deviceclass.InventoryClass
	Models                       []edgev1alpha1.ModelSpec
}

// InventoryView is the bounded read model for declared device classes joined
// with the Nodes their selectors currently match, and for the model catalog.
// The three source resourceVersions stay separate because the reads are not
// one atomic cross-resource snapshot and must not be presented as one.
type InventoryView struct {
	ReadAt                       string               `json:"readAt"`
	DeviceClassesResourceVersion string               `json:"deviceClassesResourceVersion"`
	NodesResourceVersion         string               `json:"nodesResourceVersion"`
	ModelSpecsResourceVersion    string               `json:"modelSpecsResourceVersion"`
	Classes                      []InventoryClassView `json:"classes"`
	Models                       []InventoryModelView `json:"models"`
}

type InventoryClassView struct {
	Name            string                   `json:"name"`
	UID             string                   `json:"uid"`
	ResourceVersion string                   `json:"resourceVersion"`
	NodeSelector    map[string]string        `json:"nodeSelector"`
	Architecture    string                   `json:"architecture"`
	Memory          string                   `json:"memory"`
	Accelerator     string                   `json:"accelerator,omitempty"`
	PowerModes      []InventoryPowerModeView `json:"powerModes"`
	Nodes           []InventoryNodeView      `json:"nodes"`
}

type InventoryPowerModeView struct {
	Name     string                         `json:"name"`
	Actuator edgev1alpha1.PowerModeActuator `json:"actuator"`
}

// InventoryNodeView carries Kubernetes-owned Node facts only. Ready is the
// tri-state Kubernetes reports and SchedulingDisabled is not CHILL execution
// eligibility.
type InventoryNodeView struct {
	Name               string `json:"name"`
	UID                string `json:"uid"`
	ResourceVersion    string `json:"resourceVersion"`
	Ready              string `json:"ready"`
	SchedulingDisabled bool   `json:"schedulingDisabled"`
}

type InventoryModelView struct {
	Name            string                     `json:"name"`
	UID             string                     `json:"uid"`
	ResourceVersion string                     `json:"resourceVersion"`
	Spec            edgev1alpha1.ModelSpecSpec `json:"spec"`
}

// BuildInventory projects one snapshot onto the wire model every Console
// transport shares. It computes no compatibility between a ModelSpec and a
// DeviceClass; that judgement has no authority yet.
func BuildInventory(snapshot InventorySnapshot) InventoryView {
	view := InventoryView{
		ReadAt:                       snapshot.ReadAt.Format(time.RFC3339Nano),
		DeviceClassesResourceVersion: snapshot.DeviceClassesResourceVersion,
		NodesResourceVersion:         snapshot.NodesResourceVersion,
		ModelSpecsResourceVersion:    snapshot.ModelSpecsResourceVersion,
		Classes:                      make([]InventoryClassView, 0, len(snapshot.Classes)),
		Models:                       make([]InventoryModelView, 0, len(snapshot.Models)),
	}
	for _, class := range snapshot.Classes {
		view.Classes = append(view.Classes, buildInventoryClass(class))
	}
	for _, model := range snapshot.Models {
		view.Models = append(view.Models, InventoryModelView{
			Name: model.Name, UID: string(model.UID), ResourceVersion: model.ResourceVersion, Spec: model.Spec,
		})
	}
	return view
}

func buildInventoryClass(class deviceclass.InventoryClass) InventoryClassView {
	view := InventoryClassView{
		Name: class.Name, UID: string(class.UID), ResourceVersion: class.ResourceVersion,
		NodeSelector: class.Spec.NodeSelector, Architecture: class.Spec.Architecture,
		Memory: class.Spec.MemoryBytes.String(), Accelerator: class.Spec.Accelerator,
		PowerModes: make([]InventoryPowerModeView, 0, len(class.Spec.PowerModes)),
		Nodes:      make([]InventoryNodeView, 0, len(class.Nodes)),
	}
	for _, mode := range class.Spec.PowerModes {
		view.PowerModes = append(view.PowerModes, InventoryPowerModeView{Name: mode.Name, Actuator: mode.Actuator})
	}
	for _, node := range class.Nodes {
		view.Nodes = append(view.Nodes, InventoryNodeView{
			Name: node.Name, UID: string(node.UID), ResourceVersion: node.ResourceVersion,
			Ready: string(node.Ready), SchedulingDisabled: node.SchedulingDisabled,
		})
	}
	return view
}
