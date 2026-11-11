package observation

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

// Owner-run evidence detects a lost memory tail; it is not a request/control journal.
type observationRunState struct {
	Started time.Time                `json:"started"`
	Clean   bool                     `json:"clean"`
	Unknown bool                     `json:"unknown"`
	Gaps    []serving.ObservationGap `json:"gaps"`
	// CreatedAfter retains exact successful-create provenance, not clock ordering.
	CreatedAfter map[string][]string `json:"createdAfter,omitempty"`
}

func (l *ObservationLog) startRun() error {
	if err := os.MkdirAll(l.config.Directory, 0700); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(l.config.Directory, "async-run.json"))
	var prior observationRunState
	valid := err == nil && json.Unmarshal(data, &prior) == nil && !prior.Started.IsZero()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	l.run = observationRunState{Started: l.started, Unknown: !valid || !prior.Clean || prior.Unknown, Gaps: prior.Gaps}
	if valid {
		l.run.CreatedAfter = prior.CreatedAfter
	}
	// A missing marker is not proof of a fresh installation, even in an empty directory.
	if !valid || !prior.Clean {
		lower, boot := time.Time{}, "unknown-predecessor"
		if valid {
			lower = prior.Started
			boot = prior.Started.Format(time.RFC3339Nano)
		}
		l.run.Gaps = append(l.run.Gaps, serving.ObservationGap{From: lower, To: l.started,
			Reason: "UncleanObservationTail", Boot: boot})
	}
	for _, gap := range l.run.Gaps {
		if gap.PodUID != "" && gap.ClearAt == nil {
			l.run.Unknown = true
		}
	}
	if err := l.writeRun(l.run); err != nil {
		return err
	}
	l.runCommitted = l.run
	return nil
}

func (l *ObservationLog) writeRun(state observationRunState) (retErr error) {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(l.config.Directory, ".async-run-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer func() {
		if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			retErr = errors.Join(retErr, err)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := l.syncFileOutsideLock(file); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(l.config.Directory, "async-run.json")); err != nil {
		return err
	}
	directory, err := os.Open(l.config.Directory)
	if err != nil {
		return err
	}
	return errors.Join(l.syncFileOutsideLock(directory), directory.Close())
}

func (l *ObservationLog) Recovery() serving.ObservationRecovery {
	l.submitMu.Lock()
	defer l.submitMu.Unlock()
	return serving.ObservationRecovery{Unknown: l.run.Unknown, Started: l.started}
}

// RecordRecovery records inference CLEAR separately from the scientific endpoint.
// Actor-empty cannot close a possible buffered old socket's influence interval.
func (l *ObservationLog) RecordRecovery(state serving.InstanceState, clear bool) error {
	l.submitMu.Lock()
	defer l.submitMu.Unlock()
	if err := l.submissionErrorLocked(); err != nil {
		return err
	}
	if state.PodUID == "" {
		return nil
	}
	if state.CreatedThisRun {
		l.recordCreatedPodLocked(state.PodUID)
		return nil
	}
	next := l.run
	neverStarted := state.NeverStarted && state.ID == "" &&
		!state.ObservedAt.IsZero() && !state.ObservedAt.Before(l.run.Started)
	clear = clear || neverStarted
	changed := false
	for _, root := range l.run.Gaps {
		if root.Instance != "" || root.Reason != "UncleanObservationTail" ||
			slices.Contains(l.run.CreatedAfter[state.PodUID], root.Boot) {
			continue
		}
		found := false
		for i := range next.Gaps {
			gap := &next.Gaps[i]
			if gap.Boot != root.Boot || !sameRecoveryExecutor(*gap, state) {
				continue
			}
			found = true
			bind := gap.Instance == "" && state.ID != ""
			nodeKnown := state.Node.UID != "" && (gap.Node == nil || gap.Node.UID == "")
			clearing := clear && (gap.ClearAt == nil || (state.Terminated || neverStarted) && gap.Open)
			if !bind && !nodeKnown && !clearing {
				continue
			}
			if !changed {
				next.Gaps = append([]serving.ObservationGap(nil), l.run.Gaps...)
				changed = true
			}
			gap = &next.Gaps[i]
			if bind {
				gap.Instance = state.ID
				gap.ContainerID = strings.TrimPrefix(state.ID, state.PodUID+"/")
			}
			if nodeKnown {
				node := state.Node
				gap.Node = &node
			}
			if clearing {
				l.clearRecoveryGapLocked(gap, state)
			}
		}
		if !found && l.run.Unknown {
			if !changed {
				next.Gaps = append([]serving.ObservationGap(nil), l.run.Gaps...)
				changed = true
			}
			gap := root
			gap.Instance, gap.PodUID, gap.Node = state.ID, state.PodUID, &state.Node
			gap.ContainerID = strings.TrimPrefix(state.ID, state.PodUID+"/")
			gap.Reason, gap.To, gap.Open = "UnknownExecutorInfluence", time.Time{}, true
			if clear {
				l.clearRecoveryGapLocked(&gap, state)
			}
			next.Gaps = append(next.Gaps, gap)
		}
	}
	if changed {
		l.run = next
		l.enqueueLocked(&observationBatch{run: &next})
	}
	return nil
}

func (l *ObservationLog) recordCreatedPodLocked(uid string) {
	roots := l.run.CreatedAfter[uid]
	changed := false
	for _, gap := range l.run.Gaps {
		if gap.Reason != "UncleanObservationTail" || slices.Contains(roots, gap.Boot) {
			continue
		}
		if !changed {
			roots = slices.Clone(roots)
			changed = true
		}
		roots = append(roots, gap.Boot)
	}
	if !changed {
		return
	}
	next := l.run
	next.CreatedAfter = maps.Clone(l.run.CreatedAfter)
	if next.CreatedAfter == nil {
		next.CreatedAfter = make(map[string][]string)
	}
	next.CreatedAfter[uid] = roots
	l.run = next
	l.enqueueLocked(&observationBatch{run: &next})
}

// A pending Pod can acquire its first exact executor identity, never another
// container's CLEAR. Exact termination remains proof after Node API deletion.
func sameRecoveryExecutor(gap serving.ObservationGap, state serving.InstanceState) bool {
	if gap.PodUID != state.PodUID {
		return false
	}
	if gap.Instance != state.ID && !(gap.Instance == "" && state.ID != "" && gap.Open) {
		return false
	}
	return state.Terminated || gap.Node == nil || gap.Node.UID == "" || *gap.Node == state.Node
}

func (l *ObservationLog) clearRecoveryGapLocked(gap *serving.ObservationGap, state serving.InstanceState) {
	at := state.ObservedAt
	if at.IsZero() {
		at = l.receiptLocked()
	}
	if gap.ClearAt == nil {
		gap.ClearAt = &at
	}
	if state.Terminated || state.NeverStarted && state.ID == "" && gap.Instance == "" &&
		!state.ObservedAt.IsZero() && !state.ObservedAt.Before(l.run.Started) {
		gap.To, gap.Open = at, false
	}
}

func (l *ObservationLog) recoveryGaps(from, to time.Time) []serving.ObservationGap {
	l.mu.Lock()
	state := l.runCommitted
	l.mu.Unlock()
	var result []serving.ObservationGap
	for _, original := range state.Gaps {
		// Discovery can race the successful-create notification. Keep the
		// provisional provenance, but exact creation excludes its older roots.
		if original.PodUID != "" && slices.Contains(state.CreatedAfter[original.PodUID], original.Boot) {
			continue
		}
		gap := original
		if gap.Open || gap.To.IsZero() || gap.To.After(to) {
			gap.To = to
		}
		if !from.IsZero() && gap.From.Before(from) {
			gap.From = from
		}
		if gap.From.Before(gap.To) && (from.IsZero() || gap.To.After(from)) {
			result = append(result, gap)
		}
	}
	return result
}
