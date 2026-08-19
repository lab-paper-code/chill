package operatorconsole

import (
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

// SystemsView is the bounded read model for the ChillSystems the Console can
// see. It keeps the request and the observed status separate and computes no
// aggregate health, and it carries the identity of each source so a reader can
// tell which revision the answer came from.
type SystemsView struct {
	ReadAt          string       `json:"readAt"`
	ResourceVersion string       `json:"resourceVersion"`
	Systems         []SystemView `json:"systems"`
}

type SystemView struct {
	Name              string                         `json:"name"`
	UID               string                         `json:"uid"`
	ResourceVersion   string                         `json:"resourceVersion"`
	Generation        int64                          `json:"generation"`
	CreationTimestamp string                         `json:"creationTimestamp"`
	Spec              edgev1alpha1.ChillSystemSpec   `json:"spec"`
	Status            edgev1alpha1.ChillSystemStatus `json:"status"`
}

// BuildSystems projects one snapshot onto the wire model every Console
// transport shares. It returns no error because it selects and formats fields
// rather than validating them; the query that produced the snapshot already
// owns whether the read succeeded.
func BuildSystems(snapshot SystemSnapshot) SystemsView {
	view := SystemsView{
		ReadAt: snapshot.ReadAt.Format(time.RFC3339Nano), ResourceVersion: snapshot.ResourceVersion,
		Systems: make([]SystemView, 0, len(snapshot.Systems)),
	}
	for _, system := range snapshot.Systems {
		view.Systems = append(view.Systems, SystemView{
			Name: system.Name, UID: string(system.UID), ResourceVersion: system.ResourceVersion,
			Generation: system.Generation, CreationTimestamp: system.CreationTimestamp.UTC().Format(time.RFC3339),
			Spec: system.Spec, Status: system.Status,
		})
	}
	return view
}
