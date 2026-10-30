package readapi

import (
	"net/http"
	"time"

	"github.com/lab-paper-code/chill/internal/operatorconsole"
)

const compatibilityUnknown = "Unknown"

// registerModelCompatibilityMatrix evaluates each declared path/class once on
// its name-sorted representative Node. Power modes share the static verdict.
func registerModelCompatibilityMatrix(mux *http.ServeMux, readers QueryReaders) {
	mux.HandleFunc("GET /api/v1/model-compatibility-matrix", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			writeError(w, http.StatusBadRequest, "InvalidQuery")
			return
		}
		if readers.Inventory == nil || readers.ModelCompatibility == nil {
			writeError(w, http.StatusServiceUnavailable, "ModelCompatibilityUnavailable")
			return
		}
		inventory, err := readers.Inventory(r.Context())
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "InventoryUnavailable")
			return
		}
		count := 0
		for i := range inventory.Models {
			count += len(inventory.Models[i].Spec.ExecutionPaths) * len(inventory.Classes)
		}
		rows := make([]operatorconsole.ModelCompatibilityView, 0, count)
		for i := range inventory.Models {
			model := &inventory.Models[i]
			for p := range model.Spec.ExecutionPaths {
				path := &model.Spec.ExecutionPaths[p]
				for c := range inventory.Classes {
					class := &inventory.Classes[c]
					selection := operatorconsole.ModelCompatibilitySelection{ModelSpec: model.Name, ExecutionPath: path.Name, DeviceClass: class.Name}
					snapshot := operatorconsole.ModelCompatibilitySnapshot{
						ReadAt: inventory.ReadAt, Selection: selection, Verdict: compatibilityUnknown,
						Inputs: operatorconsole.ModelCompatibilityInputs{
							ModelSpec:   operatorconsole.ModelCompatibilityObjectInput{Name: model.Name, UID: string(model.UID), ResourceVersion: model.ResourceVersion},
							DeviceClass: operatorconsole.ModelCompatibilityObjectInput{Name: class.Name, UID: string(class.UID), ResourceVersion: class.ResourceVersion},
						},
					}
					if len(class.Nodes) > 0 {
						node := &class.Nodes[0]
						for n := 1; n < len(class.Nodes); n++ {
							if class.Nodes[n].Name < node.Name {
								node = &class.Nodes[n]
							}
						}
						selection.Node = node.Name
						snapshot.Inputs.Node = operatorconsole.ModelCompatibilityObjectInput{Name: node.Name, UID: string(node.UID), ResourceVersion: node.ResourceVersion}
						resolved, readErr := readers.ModelCompatibility(r.Context(), selection)
						if readErr == nil {
							snapshot = resolved
						} else {
							snapshot.Selection = selection
							snapshot.Reasons = []operatorconsole.ModelCompatibilityReason{{Kind: compatibilityUnknown, Code: "CompatibilityReadFailed", Message: "Static support could not be read for the representative Node"}}
						}
					} else {
						snapshot.Reasons = []operatorconsole.ModelCompatibilityReason{{Kind: compatibilityUnknown, Code: "RepresentativeNodeMissing", Message: "No Node currently matches this DeviceClass"}}
					}
					if r.Context().Err() != nil {
						return
					}
					rows = append(rows, operatorconsole.BuildModelCompatibility(snapshot))
				}
			}
		}
		writeJSON(w, struct {
			ReadAt string                                   `json:"readAt"`
			Scope  string                                   `json:"scope"`
			Rows   []operatorconsole.ModelCompatibilityView `json:"rows"`
		}{inventory.ReadAt.Format(time.RFC3339Nano), "name-sorted representative Node per DeviceClass; static support only", rows})
	})
}
