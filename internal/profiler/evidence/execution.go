package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

func (e ExecutionEvidence) Validate(raw RawMeasurement) error {
	rawBytes, err := raw.CanonicalJSON(e.Intent)
	if err != nil {
		return fmt.Errorf("validate raw measurement: %w", err)
	}
	return e.validateLinkedRaw(raw.PodUID, raw.Workload, rawBytes)
}

func (e ExecutionEvidence) ValidateIdle(raw IdleRawMeasurement) error {
	rawBytes, err := raw.CanonicalJSON(e.Intent)
	if err != nil {
		return fmt.Errorf("validate idle raw measurement: %w", err)
	}
	return e.validateLinkedRaw(raw.PodUID, raw.Workload, rawBytes)
}

func (e ExecutionEvidence) validateLinkedRaw(
	rawPodUID string,
	workload *workloadobserver.Result,
	rawBytes []byte,
) error {
	if e.SchemaVersion != ExecutionEvidenceSchemaVersion {
		return fmt.Errorf("unsupported execution evidence schemaVersion %q", e.SchemaVersion)
	}
	digest, err := e.Intent.Digest()
	if err != nil {
		return fmt.Errorf("validate Run intent: %w", err)
	}
	if e.IntentDigest != digest {
		return errors.New("execution evidence intent digest mismatch")
	}
	if !json.Valid(e.CandidateReportBytes) {
		return errors.New("candidate report is not valid JSON")
	}
	if err := profilingrun.ValidateCandidateReport(e.CandidateReportBytes, e.Intent); err != nil {
		return fmt.Errorf("validate candidate report: %w", err)
	}
	if err := e.RawMeasurement.Validate(); err != nil {
		return fmt.Errorf("validate raw measurement identity: %w", err)
	}
	if err := verifyIdentityBytes(e.RawMeasurement, rawBytes); err != nil {
		return err
	}
	if err := e.Terminal.validate(e.Intent, rawPodUID); err != nil {
		return err
	}
	if workload != nil && (workload.Run.Namespace != e.Terminal.Pod.Namespace ||
		workload.Run.JobUID != e.Terminal.Job.UID || workload.Run.PodName != e.Terminal.Pod.Name ||
		workload.Run.PodUID != e.Terminal.Pod.UID) {
		return errors.New("workload observation Run identity does not match terminal Kubernetes facts")
	}
	if e.FinalizedAt.IsZero() || e.FinalizedAt.Before(e.Terminal.Job.CompletionTime) {
		return errors.New("execution evidence finalization time is invalid")
	}
	return nil
}

func (e ExecutionEvidence) CanonicalJSON(raw RawMeasurement) ([]byte, error) {
	if err := e.Validate(raw); err != nil {
		return nil, err
	}
	return e.canonicalJSON()
}

func (e ExecutionEvidence) CanonicalIdleJSON(raw IdleRawMeasurement) ([]byte, error) {
	if err := e.ValidateIdle(raw); err != nil {
		return nil, err
	}
	return e.canonicalJSON()
}

func (e ExecutionEvidence) canonicalJSON() ([]byte, error) {
	canonical := e
	canonical.FinalizedAt = canonical.FinalizedAt.UTC()
	canonical.Terminal.Job.CompletionTime = canonical.Terminal.Job.CompletionTime.UTC()
	canonical.Terminal.NodeWorkload.ObservedAt = canonical.Terminal.NodeWorkload.ObservedAt.UTC()
	canonical.Terminal.Containers = append([]ContainerTerminal(nil), e.Terminal.Containers...)
	for i := range canonical.Terminal.Containers {
		canonical.Terminal.Containers[i].StartedAt = canonical.Terminal.Containers[i].StartedAt.UTC()
		canonical.Terminal.Containers[i].FinishedAt = canonical.Terminal.Containers[i].FinishedAt.UTC()
	}
	sort.Slice(canonical.Terminal.Containers, func(i, j int) bool {
		return canonical.Terminal.Containers[i].Name < canonical.Terminal.Containers[j].Name
	})
	canonical.Terminal.NodeWorkload.Pods = append([]WorkloadPod(nil), e.Terminal.NodeWorkload.Pods...)
	for i := range canonical.Terminal.NodeWorkload.Pods {
		canonical.Terminal.NodeWorkload.Pods[i].Images = append([]string(nil), canonical.Terminal.NodeWorkload.Pods[i].Images...)
		sort.Strings(canonical.Terminal.NodeWorkload.Pods[i].Images)
	}
	sort.Slice(canonical.Terminal.NodeWorkload.Pods, func(i, j int) bool {
		left := canonical.Terminal.NodeWorkload.Pods[i]
		right := canonical.Terminal.NodeWorkload.Pods[j]
		return left.Namespace+"\x00"+left.Name+"\x00"+left.UID < right.Namespace+"\x00"+right.Name+"\x00"+right.UID
	})
	return json.Marshal(canonical)
}

func verifyIdentityBytes(identity evidencestore.ObjectIdentity, payload []byte) error {
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if identity.Digest != digest || identity.Size != int64(len(payload)) {
		return errors.New("raw measurement identity does not match canonical bytes")
	}
	return nil
}
