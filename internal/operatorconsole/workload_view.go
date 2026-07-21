package operatorconsole

import (
	"fmt"
	"sort"
	"time"

	"github.com/lab-paper-code/chill/internal/profilingrun"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

// EvidencePassportWorkload is a bounded read projection of the retained
// measurement-window workload evidence. It does not replace the raw result.
type EvidencePassportWorkload struct {
	NodeName          string                               `json:"nodeName"`
	CoverageStartedAt time.Time                            `json:"coverageStartedAt"`
	CoverageEndedAt   time.Time                            `json:"coverageEndedAt"`
	Continuous        bool                                 `json:"continuous"`
	InitialActivePods int                                  `json:"initialActivePods"`
	FinalActivePods   int                                  `json:"finalActivePods"`
	MaterialEvents    int                                  `json:"materialEvents"`
	FailureReason     string                               `json:"failureReason,omitempty"`
	CoResidents       []EvidencePassportWorkloadCoResident `json:"coResidents"`
}

type EvidencePassportWorkloadCoResident struct {
	Namespace        string                         `json:"namespace"`
	Name             string                         `json:"name"`
	UID              string                         `json:"uid"`
	Phase            string                         `json:"phase"`
	Owner            *EvidencePassportWorkloadOwner `json:"owner,omitempty"`
	Images           []string                       `json:"images"`
	PresentAtStart   bool                           `json:"presentAtStart"`
	PresentAtEnd     bool                           `json:"presentAtEnd"`
	EventKinds       []string                       `json:"eventKinds,omitempty"`
	CHILLMeasurement bool                           `json:"chillMeasurement"`
	Changed          bool                           `json:"changed"`
}

type EvidencePassportWorkloadOwner struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	UID  string `json:"uid"`
}

type workloadAccumulator struct {
	view       EvidencePassportWorkloadCoResident
	eventKinds map[string]struct{}
}

func summarizeWorkload(
	intent profilingrun.Intent,
	result *workloadobserver.Result,
) (*EvidencePassportWorkload, error) {
	if result == nil {
		return nil, nil
	}
	if err := result.Validate(); err != nil {
		return nil, fmt.Errorf("validate workload evidence for passport: %w", err)
	}
	digest, err := intent.Workload.Digest()
	if err != nil {
		return nil, fmt.Errorf("validate workload contract for passport: %w", err)
	}
	if digest != result.PolicyDigest {
		return nil, fmt.Errorf("workload contract digest does not match evidence")
	}

	observed := make(map[string]*workloadAccumulator)
	add := func(pod workloadobserver.Pod, atStart, atEnd bool, eventKind string) {
		if pod.UID == result.Run.PodUID {
			return
		}
		entry, exists := observed[pod.UID]
		if !exists {
			entry = &workloadAccumulator{view: EvidencePassportWorkloadCoResident{
				Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID,
			}, eventKinds: map[string]struct{}{}}
			observed[pod.UID] = entry
		}
		entry.view.Phase = pod.Phase
		entry.view.Images = append([]string(nil), pod.Images...)
		entry.view.Owner = workloadOwner(pod.Owner)
		entry.view.PresentAtStart = entry.view.PresentAtStart || atStart
		entry.view.PresentAtEnd = entry.view.PresentAtEnd || atEnd
		if eventKind != "" {
			entry.eventKinds[eventKind] = struct{}{}
			entry.view.Changed = true
		}
		ownerKind := ""
		if pod.Owner != nil {
			ownerKind = pod.Owner.Kind
		}
		entry.view.CHILLMeasurement = entry.view.CHILLMeasurement ||
			profilingrun.IsMeasurementWorkload(ownerKind, pod.Labels)
	}
	for _, pod := range result.InitialActivePods {
		add(pod, true, false, "")
	}
	for _, event := range result.Events {
		add(event.Pod, false, false, string(event.Kind))
	}
	for _, pod := range result.FinalActivePods {
		add(pod, false, true, "")
	}

	coResidents := make([]EvidencePassportWorkloadCoResident, 0, len(observed))
	for _, entry := range observed {
		for kind := range entry.eventKinds {
			entry.view.EventKinds = append(entry.view.EventKinds, kind)
		}
		sort.Strings(entry.view.EventKinds)
		coResidents = append(coResidents, entry.view)
	}
	sort.Slice(coResidents, func(i, j int) bool {
		left := coResidents[i].Namespace + "\x00" + coResidents[i].Name + "\x00" + coResidents[i].UID
		right := coResidents[j].Namespace + "\x00" + coResidents[j].Name + "\x00" + coResidents[j].UID
		return left < right
	})

	view := &EvidencePassportWorkload{
		NodeName:          result.Target.NodeName,
		CoverageStartedAt: result.CoverageStartedAt.UTC(), CoverageEndedAt: result.CoverageEndedAt.UTC(),
		Continuous: result.Continuous, InitialActivePods: len(result.InitialActivePods),
		FinalActivePods: len(result.FinalActivePods), MaterialEvents: len(result.Events),
		CoResidents: coResidents,
	}
	if result.Failure != nil {
		view.FailureReason = string(result.Failure.Reason)
	}
	return view, nil
}

func workloadOwner(owner *workloadobserver.OwnerIdentity) *EvidencePassportWorkloadOwner {
	if owner == nil {
		return nil
	}
	return &EvidencePassportWorkloadOwner{Kind: owner.Kind, Name: owner.Name, UID: owner.UID}
}
