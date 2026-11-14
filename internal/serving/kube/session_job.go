package kube

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	"k8s.io/apimachinery/pkg/api/resource"
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
	levels := 2
	if load.batch == 1 {
		levels++
		args = append(args, "--sparse")
	}
	// Each non-saturation level can also spend one level limit waiting for
	// physical idle after an expired request.
	levelBudget := float64(2*levels-1) * s.LevelDurationLimitSeconds
	deadlineSeconds := int64(math.Ceil(levelBudget + float64(s.RequestLifetimeSeconds) + load.config.RunnerTimeout.Seconds()))
	resources := core.ResourceRequirements{
		Requests: core.ResourceList{core.ResourceMemory: resource.MustParse("512Mi")},
		Limits:   core.ResourceList{core.ResourceMemory: resource.MustParse("512Mi")},
	}
	data := core.VolumeMount{Name: "data", MountPath: "/data"}
	labels := map[string]string{loadNodeLabel: ref.UID}
	return &batch.Job{
		ObjectMeta: meta.ObjectMeta{Name: "session-" + hex.EncodeToString(sum[:10]) + "-b" + strconv.Itoa(load.batch), Namespace: load.config.Namespace, Labels: labels},
		Spec: batch.JobSpec{
			BackoffLimit: ptr.To[int32](0), ActiveDeadlineSeconds: ptr.To(deadlineSeconds),
			Template: core.PodTemplateSpec{
				ObjectMeta: meta.ObjectMeta{Labels: labels},
				Spec: core.PodSpec{
					RestartPolicy: core.RestartPolicyNever, NodeName: load.config.NodeName, ServiceAccountName: load.config.ServiceAccount,
					AutomountServiceAccountToken: ptr.To(false),
					InitContainers: []core.Container{{Name: "dataset", Image: prep.Dataset.Image, VolumeMounts: []core.VolumeMount{data}, Resources: resources,
						Command: []string{"sh", "-ec", `cp -R "$1"/. /data/`, "--", path.Join(prep.Dataset.Path, "profiling")}}},
					Containers: []core.Container{{Name: sessionLoadContainer, Image: load.config.Image, VolumeMounts: []core.VolumeMount{data}, Args: args, Resources: resources}},
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
	EndReason               string    `json:"endReason"`
	Detail                  string    `json:"detail,omitempty"`
}

func decodeSessionLevelTable(message string) (sessionLevelTable, error) {
	var table sessionLevelTable
	if err := json.Unmarshal([]byte(message), &table); err != nil {
		return table, fmt.Errorf("session-load termination message is not a level table: %w", err)
	}
	return table, nil
}

func (l sessionLevelWire) row() profilederivation.DriverLevelRow {
	row := profilederivation.DriverLevelRow{Kind: l.Kind, ID: l.WorkloadUID, Start: l.StartedAt, End: l.EndedAt,
		SentInputs: l.SentInputs, CompletedInputs: l.CompletedInputs, CompletedBatches: l.CompletedBatches, EndReason: l.EndReason}
	if l.EndReason != sessionSamplesReached {
		row.Detail = l.Detail
	}
	row.Excluded = l.EndReason == sessionRequestLifetimeExpired
	return row
}

// runSessionLoad creates the load Job, waits for it to end and returns the
// load container's level table. Cleanup deletes the Job on every exit.
// A Job that ends without a level table fails with its Pod's failure reason.
// prestartTimeout bounds creation and startup only, until the load container runs.
func (c *SchedulerController) runSessionLoad(ctx context.Context, job *batch.Job, prestartTimeout time.Duration) (table sessionLevelTable, err error) {
	startCtx, cancelStart := context.WithTimeout(ctx, prestartTimeout)
	defer cancelStart()
	jobs := c.kube.BatchV1().Jobs(job.Namespace)
	if _, err := jobs.Create(startCtx, job, meta.CreateOptions{}); err != nil {
		return sessionLevelTable{}, err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		err = errors.Join(err, servingstate.IgnoreNotFound(jobs.Delete(cleanupCtx, job.Name, meta.DeleteOptions{PropagationPolicy: ptr.To(meta.DeletePropagationBackground)})))
	}()
	var ended *batch.JobCondition
	var refused string
	var lastPod *core.Pod
	started := false
	wait := func(waitCtx context.Context, untilStarted bool) error {
		return c.operations.Wait(waitCtx, func() (bool, error) {
			current, err := jobs.Get(waitCtx, job.Name, meta.GetOptions{})
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
			pod, err := c.sessionLoadPod(waitCtx, job)
			if err != nil || pod == nil {
				return false, err
			}
			lastPod = pod
			if failure, failed := servingstate.ClassifyPreparationFailure(pod, nil, nil, nil, false); failed && failure.Classification == serving.PreparationDeterministic {
				refused = failure.Reason
				return true, nil
			}
			for _, status := range pod.Status.ContainerStatuses {
				if status.Name == sessionLoadContainer && (status.State.Running != nil || status.State.Terminated != nil) {
					started = true
				}
			}
			return untilStarted && started, nil
		})
	}
	err = wait(startCtx, true)
	cancelStart()
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		var detail strings.Builder
		if lastPod != nil {
			for _, statuses := range [][]core.ContainerStatus{lastPod.Status.InitContainerStatuses, lastPod.Status.ContainerStatuses} {
				for _, status := range statuses {
					if waiting := status.State.Waiting; waiting != nil {
						fmt.Fprintf(&detail, "; %s: %s %s", status.Name, waiting.Reason, waiting.Message)
					}
				}
			}
		}
		return sessionLevelTable{}, fmt.Errorf("session-load did not start within %s: %w%s", prestartTimeout, err, detail.String())
	}
	if err == nil && ended == nil && refused == "" {
		err = wait(ctx, false)
	}
	if err != nil {
		return sessionLevelTable{}, err
	}
	if refused != "" {
		return sessionLevelTable{}, errors.New(refused)
	}
	return c.sessionLoadResult(ctx, job, ended)
}

func (c *SchedulerController) sessionLoadResult(ctx context.Context, job *batch.Job, ended *batch.JobCondition) (sessionLevelTable, error) {
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
