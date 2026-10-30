package operatorconsole

import (
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ProfilingRunsSnapshot is one namespaced or all-namespaces LIST response. An
// empty Namespace means all namespaces; it does not imply ChillSystem ownership.
type ProfilingRunsSnapshot struct {
	ReadAt          time.Time
	ResourceVersion string
	Namespace       string
	Runs            []edgev1alpha1.ProfilingRun
}

// ProfilingRunsView is the bounded read model for a ProfilingRun listing. It
// keeps each Run's immutable spec and its status separate, retains the source
// resourceVersion, and states its own scope rather than letting a reader infer
// one from the namespace.
type ProfilingRunsView struct {
	ReadAt          string             `json:"readAt"`
	ResourceVersion string             `json:"resourceVersion"`
	Namespace       string             `json:"namespace,omitempty"`
	AllNamespaces   bool               `json:"allNamespaces"`
	Runs            []ProfilingRunView `json:"runs"`
}

type ProfilingRunView struct {
	Namespace         string                          `json:"namespace"`
	Name              string                          `json:"name"`
	UID               string                          `json:"uid"`
	ResourceVersion   string                          `json:"resourceVersion"`
	Generation        int64                           `json:"generation"`
	CreationTimestamp string                          `json:"creationTimestamp"`
	OwnerReferences   []metav1.OwnerReference         `json:"ownerReferences"`
	Labels            map[string]string               `json:"labels"`
	Spec              edgev1alpha1.ProfilingRunSpec   `json:"spec"`
	Status            edgev1alpha1.ProfilingRunStatus `json:"status"`
}

// BuildProfilingRuns projects one snapshot onto the wire model every Console
// transport shares. An empty snapshot Namespace means the read covered every
// namespace; it never implies ChillSystem ownership.
func BuildProfilingRuns(snapshot ProfilingRunsSnapshot) ProfilingRunsView {
	view := ProfilingRunsView{
		ReadAt: snapshot.ReadAt.Format(time.RFC3339Nano), ResourceVersion: snapshot.ResourceVersion,
		Namespace: snapshot.Namespace, AllNamespaces: snapshot.Namespace == "",
		Runs: make([]ProfilingRunView, 0, len(snapshot.Runs)),
	}
	for _, run := range snapshot.Runs {
		owners := make([]metav1.OwnerReference, len(run.OwnerReferences))
		for index := range run.OwnerReferences {
			run.OwnerReferences[index].DeepCopyInto(&owners[index])
		}
		labels := make(map[string]string)
		for _, key := range []string{
			"edge.dacs.io/batch-energy-optimization", "edge.dacs.io/workload-mode",
			"edge.dacs.io/batch", "edge.dacs.io/attempt",
		} {
			if value, exists := run.Labels[key]; exists {
				labels[key] = value
			}
		}
		view.Runs = append(view.Runs, ProfilingRunView{
			Namespace: run.Namespace, Name: run.Name, UID: string(run.UID),
			ResourceVersion: run.ResourceVersion, Generation: run.Generation,
			CreationTimestamp: run.CreationTimestamp.UTC().Format(time.RFC3339),
			OwnerReferences:   owners, Labels: labels,
			Spec: run.Spec, Status: run.Status,
		})
	}
	return view
}
