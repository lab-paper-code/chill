package kube

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	batch "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// sessionLoadContainer is the load Job container whose termination message
// is the level table.
const sessionLoadContainer = "load"

// sessionLoad is what one batch size's load Job needs besides the session.
type sessionLoad struct {
	batch int
	// podUID names the instance Service chill-executor-<PodUID>.
	podUID, servingNamespace string
	minimumSamples           int
	config                   SessionConfig
	preparation              SessionPreparation
	actor                    sessionActorLimits
}

// sessionActorLimits are the native queue limits the load must stay within.
type sessionActorLimits struct{ maxQueueRequests, maxQueueBytes int }

// sessionLoadJob runs chill-server session-load for one batch size on the
// ChillServer Node against the instance Service. Every request carries the
// workload identifier <BEO UID>/<batch>/<level>.
func sessionLoadJob(beo *edge.BatchEnergyOptimization, ref serving.NodeReference, load sessionLoad) *batch.Job {
	s, prep := beo.Spec.Session, load.preparation
	sum := sha256.Sum256([]byte(beo.UID))
	seconds := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) + "s" }
	args := []string{"session-load",
		"--native-endpoint", "http://" + servingstate.InstanceServicePrefix + load.podUID + "." + load.servingNamespace + ":8080",
		"--model", prep.RunConfig.State.Model,
		"--fields", strings.Join(prep.RunConfig.State.Preprocessor.Inputs, ","),
		"--dataset-dir", "/data",
		"--batch", strconv.Itoa(load.batch),
		"--session", string(beo.UID),
		"--definition", prep.RunConfig.Measurement.WorkloadResourceVersion,
		"--slo-percentile", strconv.FormatFloat(prep.SLOPercentile, 'f', -1, 64),
		"--slo-target", prep.SLOTarget.String(),
		"--request-lifetime", (time.Duration(s.RequestLifetimeSeconds) * time.Second).String(),
		"--delivery-margin", (time.Duration(s.DeliveryMarginMilliseconds) * time.Millisecond).String(),
		"--min-samples", strconv.Itoa(load.minimumSamples),
		"--meter-lag", seconds(s.MeterLagSeconds),
		"--min-power-window", seconds(s.MinimumPowerWindowSeconds),
		"--mid-ratio", strconv.FormatFloat(s.MidLevelRatio, 'f', -1, 64),
		"--sparse-mean-gap", seconds(s.SparseMeanGapSeconds),
		"--level-limit", seconds(s.LevelDurationLimitSeconds),
		"--max-queue-requests", strconv.Itoa(load.actor.maxQueueRequests),
		"--max-queue-bytes", strconv.Itoa(load.actor.maxQueueBytes),
		"--request-upper-bytes", strconv.FormatInt(s.FramedDecodedRequestUpperBytes, 10),
	}
	// Serving runs a long idle gap's batch at size 1 only.
	if load.batch == 1 {
		args = append(args, "--sparse")
	}
	data := core.VolumeMount{Name: "data", MountPath: "/data"}
	labels := map[string]string{loadNodeLabel: ref.UID}
	return &batch.Job{
		ObjectMeta: meta.ObjectMeta{Name: "session-" + hex.EncodeToString(sum[:10]) + "-b" + strconv.Itoa(load.batch), Namespace: load.config.Namespace, Labels: labels},
		Spec: batch.JobSpec{
			BackoffLimit: ptr.To[int32](0),
			Template: core.PodTemplateSpec{
				ObjectMeta: meta.ObjectMeta{Labels: labels},
				Spec: core.PodSpec{
					RestartPolicy: core.RestartPolicyNever, NodeName: load.config.NodeName, ServiceAccountName: load.config.ServiceAccount,
					AutomountServiceAccountToken: ptr.To(false),
					InitContainers: []core.Container{{Name: "dataset", Image: prep.Dataset.Image, VolumeMounts: []core.VolumeMount{data},
						Command: []string{"sh", "-ec", `cp -R "$1"/. /data/`, "--", path.Join(prep.Dataset.Path, "profiling")}}},
					Containers: []core.Container{{Name: sessionLoadContainer, Image: load.config.Image, VolumeMounts: []core.VolumeMount{data}, Args: args}},
					Volumes:    []core.Volume{{Name: "data", VolumeSource: core.VolumeSource{EmptyDir: &core.EmptyDirVolumeSource{}}}},
				},
			},
		},
	}
}

// sessionLevelTable is the termination message session-load writes.
type sessionLevelTable struct {
	Error  string             `json:"error,omitempty"`
	Levels []sessionLevelWire `json:"levels"`
}

type sessionLevelWire struct {
	Kind                    string    `json:"kind"`
	WorkloadUID             string    `json:"workloadUID"`
	StartedAt               time.Time `json:"startedAt"`
	EndedAt                 time.Time `json:"endedAt"`
	SentInputs              int       `json:"sentInputs"`
	CompletedInputs         int       `json:"completedInputs"`
	CompletedBatches        int       `json:"completedBatches"`
	Outstanding             int       `json:"outstanding,omitempty"`
	TargetInputsPerSecond   float64   `json:"targetInputsPerSecond,omitempty"`
	AchievedInputsPerSecond float64   `json:"achievedInputsPerSecond"`
	PredictedSeconds        float64   `json:"predictedSeconds,omitempty"`
	LimitSeconds            float64   `json:"limitSeconds,omitempty"`
	EndReason               string    `json:"endReason"`
	Detail                  string    `json:"detail,omitempty"`
}

func decodeSessionLevelTable(message string) (sessionLevelTable, error) {
	var table sessionLevelTable
	decoder := json.NewDecoder(bytes.NewReader([]byte(message)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&table); err != nil {
		return table, fmt.Errorf("session-load termination message is not a level table: %w", err)
	}
	return table, nil
}

func (l sessionLevelWire) row() profilederivation.DriverLevelRow {
	row := profilederivation.DriverLevelRow{Kind: l.Kind, ID: l.WorkloadUID, Start: l.StartedAt, End: l.EndedAt,
		SentInputs: l.SentInputs, CompletedInputs: l.CompletedInputs, CompletedBatches: l.CompletedBatches, EndReason: l.EndReason}
	if l.EndReason == sessionPredictedOverLimit {
		row.PredictedSeconds, row.LimitSeconds = ptr.To(l.PredictedSeconds), ptr.To(l.LimitSeconds)
	}
	return row
}

// runSessionLoad creates the load Job, waits for it to end and returns the
// load container's level table. The Job is deleted on every path. A Job that
// ends without a level table fails with its Pod's failure reason.
func (c *SchedulerController) runSessionLoad(ctx context.Context, job *batch.Job) (sessionLevelTable, error) {
	jobs := c.kube.BatchV1().Jobs(job.Namespace)
	if _, err := jobs.Create(ctx, job, meta.CreateOptions{}); err != nil {
		return sessionLevelTable{}, err
	}
	defer func() {
		// Background propagation removes the Pod; the default orphans it.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = servingstate.IgnoreNotFound(jobs.Delete(cleanupCtx, job.Name, meta.DeleteOptions{PropagationPolicy: ptr.To(meta.DeletePropagationBackground)}))
	}()
	var ended *batch.JobCondition
	var refused string
	err := c.operations.Wait(ctx, func() (bool, error) {
		current, err := jobs.Get(ctx, job.Name, meta.GetOptions{})
		if err != nil {
			return false, err
		}
		for i, condition := range current.Status.Conditions {
			if (condition.Type == batch.JobComplete || condition.Type == batch.JobFailed) && condition.Status == core.ConditionTrue {
				ended = &current.Status.Conditions[i]
			}
		}
		if ended != nil {
			return true, nil
		}
		// A Pod that cannot start (an image or permission refusal) never ends the Job.
		pod, err := c.sessionLoadPod(ctx, job)
		if err != nil || pod == nil {
			return false, err
		}
		if failure, failed := servingstate.ClassifyPreparationFailure(pod, nil, nil, nil, false); failed && failure.Classification == serving.PreparationDeterministic {
			refused = failure.Reason
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return sessionLevelTable{}, err
	}
	if refused != "" {
		return sessionLevelTable{}, errors.New(refused)
	}
	pod, err := c.sessionLoadPod(ctx, job)
	if err != nil {
		return sessionLevelTable{}, err
	}
	var terminated *core.ContainerStateTerminated
	if pod != nil {
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == sessionLoadContainer {
				terminated = status.State.Terminated
			}
		}
	}
	if terminated != nil && terminated.Message != "" {
		if table, decodeErr := decodeSessionLevelTable(terminated.Message); decodeErr == nil || ended.Type == batch.JobComplete {
			return table, decodeErr
		}
	}
	switch {
	case ended.Type == batch.JobComplete:
		return sessionLevelTable{}, errors.New("session-load completed without a level table")
	case terminated != nil:
		return sessionLevelTable{}, fmt.Errorf("session-load %s (exit %d) %s", terminated.Reason, terminated.ExitCode, strings.TrimSpace(terminated.Message))
	case pod != nil && pod.Status.Reason != "":
		return sessionLevelTable{}, fmt.Errorf("session-load Pod %s: %s", pod.Status.Reason, pod.Status.Message)
	}
	return sessionLevelTable{}, fmt.Errorf("session-load Job %s: %s", ended.Reason, ended.Message)
}

// sessionLoadPod is the load Job's Pod, nil before the Job created it.
func (c *SchedulerController) sessionLoadPod(ctx context.Context, job *batch.Job) (*core.Pod, error) {
	pods, err := c.kube.CoreV1().Pods(job.Namespace).List(ctx, meta.ListOptions{LabelSelector: "job-name=" + job.Name})
	if err != nil || len(pods.Items) == 0 {
		return nil, err
	}
	return &pods.Items[0], nil
}
