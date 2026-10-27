package main

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/live"
	ctrl "sigs.k8s.io/controller-runtime"
)

// livePublisher projects committed callback arguments. It never polls REST or
// rereads the controller from a decision/action callback.
type livePublisher struct {
	hub            *live.Hub
	metrics        *live.Metrics
	mu             sync.Mutex
	registered     map[string]live.Workload
	autonomous     []string
	instances      map[string]bool
	actions        []live.Action
	meters, active []string
	powerKnown     bool
	changed        chan struct{}
}

func newLivePublisher(ctx context.Context) *livePublisher {
	now := time.Now().UTC()
	h := live.New(now)
	p := &livePublisher{hub: h, metrics: live.NewMetrics(h, now), registered: map[string]live.Workload{}, instances: map[string]bool{}, changed: make(chan struct{}, 1)}
	go func() {
		if err := h.Run(ctx, p.metrics); err != nil && ctx.Err() == nil {
			ctrl.Log.Error(err, "run live channel")
		}
	}()
	return p
}

func liveAt(at time.Time) string                    { return at.UTC().Format(time.RFC3339Nano) }
func liveNode(n serving.NodeReference) live.NodeRef { return live.NodeRef{UID: n.UID, Name: n.Name} }
func (p *livePublisher) publish(at time.Time, delta live.Delta, event *live.Event) {
	if err := p.hub.Publish(at, delta, event); err != nil {
		ctrl.Log.Error(err, "publish recorded live projection")
	}
}
func (p *livePublisher) changedRuntime() {
	select {
	case p.changed <- struct{}{}:
	default:
	}
}
func (p *livePublisher) startRuntime(ctx context.Context, runtime *serving.Runtime) {
	p.runtime(runtime)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		dirty := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.changed:
				dirty = true
			case <-ticker.C:
				if dirty {
					p.runtime(runtime)
					dirty = false
				}
			}
		}
	}()
}

func (p *livePublisher) workload(view servingkube.WorkloadView, removed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w := view.Desired
	uid := string(w.UID)
	if removed {
		delete(p.registered, uid)
	} else {
		row := live.Workload{UID: uid, Name: w.Name, Model: w.Spec.ModelRef.Name,
			SLO:       live.SLO{Percentile: w.Spec.SLO.Percentile, TargetLatencyNanos: int64(w.Spec.SLO.TargetLatency.Duration)},
			Readiness: live.Readiness{Reason: view.Reason, ObservedAt: liveAt(view.ObservedAt)}}
		if view.Applied != nil {
			row = liveWorkload(serving.WorkloadState{Applied: view.Applied, Admission: view.Admission, Reason: view.Reason, ObservedAt: view.ObservedAt})
		}
		row.Autonomous = slices.Contains(p.autonomous, uid)
		p.registered[uid] = row
	}
	p.publishWorkloads(time.Now().UTC())
}
func liveWorkload(state serving.WorkloadState) live.Workload {
	d := state.Applied
	id := d.Identity()
	return live.Workload{UID: d.UID, Name: d.Name, Model: d.Model.Name, DefinitionID: &id,
		SLO: live.SLO{Percentile: d.SLO.Percentile, TargetLatencyNanos: int64(d.SLO.TargetLatency)}, Admission: state.Admission,
		Readiness: live.Readiness{Ready: state.Reason == "Ready", Reason: state.Reason, ObservedAt: liveAt(state.ObservedAt)}}
}
func (p *livePublisher) publishWorkloads(at time.Time) {
	rows := make([]live.Workload, 0, len(p.registered))
	uids := make([]string, 0, len(p.registered))
	for uid, row := range p.registered {
		rows = append(rows, row)
		uids = append(uids, uid)
	}
	slices.SortFunc(rows, func(a, b live.Workload) int { return strings.Compare(a.UID, b.UID) })
	p.metrics.SetWorkloads(uids)
	p.publish(at, live.Delta{Workloads: &rows}, nil)
}
func (p *livePublisher) runtime(runtime *serving.Runtime) {
	states, physical, routing := runtime.Current()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, state := range states {
		row := liveWorkload(state)
		row.Autonomous = slices.Contains(p.autonomous, row.UID)
		p.registered[row.UID] = row
	}
	p.publishWorkloads(time.Now().UTC())
	rows := make([]live.Instance, 0, len(physical))
	present := map[string]bool{}
	active := map[string]bool{}
	for _, i := range physical {
		present[i.ID] = true
		state := "preparing"
		uid := i.OwnerWorkloadUID
		if len(i.Workloads) > 0 {
			uid = i.Workloads[0].UID
		}
		if i.Ready {
			state = "serving"
		}
		if len(i.Workloads) == 0 && i.Ready {
			state = "draining"
		}
		if i.Stopped {
			state = "removing"
		}
		if i.OwnerWorkloadUID != "" || len(i.Workloads) > 0 {
			active[i.Node.UID] = true
		}
		rows = append(rows, live.Instance{ID: i.ID, Node: liveNode(i.Node), WorkloadUID: uid, OwnerWorkloadUID: i.OwnerWorkloadUID,
			ProfileID: i.Profile, Ready: i.Ready, Stopped: i.Stopped, State: state,
			Routable: maps.Clone(i.Routable), Running: maps.Clone(i.Running), ObservedAt: liveAt(i.ObservedAt)})
	}
	removed := &live.Removed{}
	for id := range p.instances {
		if !present[id] {
			removed.Instances = append(removed.Instances, id)
		}
	}
	p.instances = present
	p.active = p.active[:0]
	for uid := range active {
		p.active = append(p.active, uid)
	}
	p.metrics.SetPowerScope(p.meters, p.active, p.powerKnown)
	routes := live.Routing{}
	for uid, weights := range routing {
		routes[uid] = map[string]float64{}
		for id, weight := range weights {
			routes[uid][id] = float64(weight)
		}
	}
	p.publish(time.Now().UTC(), live.Delta{Instances: &rows, Routing: &routes, Removed: removed}, nil)
}
func (p *livePublisher) decision(d serving.SchedulerDecision, snapshot serving.SchedulerSnapshot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.autonomous = slices.Clone(d.Settings.AutonomousWorkloads)
	for uid, row := range p.registered {
		row.Autonomous = slices.Contains(p.autonomous, uid)
		p.registered[uid] = row
	}
	p.publishWorkloads(d.At)
	nodes := make([]live.Node, 0, len(snapshot.Nodes))
	for _, n := range snapshot.Nodes {
		at := liveAt(n.ObservedAt)
		nodes = append(nodes, live.Node{UID: n.Node.UID, Name: n.Node.Name, PowerMode: n.PowerMode, ObservedAt: &at})
	}
	decision := &live.Decision{At: liveAt(d.At), State: d.Status, Reason: strings.Join(d.Reasons, "; ")}
	p.publish(d.At, live.Delta{Nodes: &nodes, Decision: &decision}, &live.Event{Kind: "decision", Decision: decision})
}
func (p *livePublisher) action(r servingkube.OperationResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	uid := r.Operation.WorkloadUID
	if uid == "" && r.Operation.Prepare != nil && r.Operation.Prepare.Owner != nil {
		uid = r.Operation.Prepare.Owner.UID
	}
	a := live.Action{ID: r.Operation.ID, Kind: r.Operation.Kind, Node: liveNode(r.Operation.Node), WorkloadUID: uid, Status: r.Status,
		StartedAt: liveAt(r.StartedAt), ExpectedSeconds: r.Operation.ExpectedSeconds}
	if source := r.Operation.ExpectedSource; source != nil {
		ref, _ := json.Marshal(source.Reference)
		a.ExpectedSource = &live.ExpectedSource{CostID: source.CostID, Reference: ref, Statistic: source.Statistic, TrialCount: source.TrialCount}
	}
	phase := "started"
	if !r.EndedAt.IsZero() {
		at := liveAt(r.EndedAt)
		a.CompletedAt = &at
		phase = "completed"
		if r.Status != "complete" {
			phase = "failed"
		}
	}
	index := slices.IndexFunc(p.actions, func(old live.Action) bool { return old.Key() == a.Key() })
	if index < 0 {
		p.actions = append(p.actions, a)
	} else {
		p.actions[index] = a
	}
	removed := &live.Removed{}
	// ponytail: 100 recent action summaries; the durable REST stream owns history.
	if len(p.actions) > 100 {
		removed.Actions = []string{p.actions[0].Key()}
		p.actions = slices.Clone(p.actions[1:])
	}
	rows := []live.Action{a}
	event := &live.ActionEvent{Action: a, Phase: phase}
	if phase != "started" {
		event.Result = &live.ActionResult{Status: r.Status, Error: r.Error}
	}
	p.publish(time.Now().UTC(), live.Delta{Actions: &rows, Removed: removed}, &live.Event{Kind: "action", Action: event})
}
func (p *livePublisher) request(event serving.RequestEvent) {
	if event.WorkloadUID == "" {
		return // Unknown names have no Workload UID cohort.
	}
	switch event.Kind {
	case "received":
		p.metrics.Received(event.WorkloadUID, event.At)
	case "response_finished":
		latency := time.Duration(event.ElapsedNanos)
		var measured *time.Duration
		if event.ElapsedNanos >= 0 {
			measured = &latency
		}
		var slo time.Duration
		if event.SLO != nil {
			slo = event.SLO.TargetLatency
		}
		p.metrics.Finished(event.WorkloadUID, event.At, event.Outcome == "completed", measured, slo)
	}
}
func (p *livePublisher) powerScope(meters []servingkube.MeterDeclaration, enabled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.meters = nil
	for _, meter := range meters {
		if !slices.Contains(p.meters, meter.Node.UID) {
			p.meters = append(p.meters, meter.Node.UID)
		}
	}
	p.powerKnown = enabled && len(meters) > 0
	p.metrics.SetPowerScope(p.meters, p.active, p.powerKnown)
}

type livePowerSource struct {
	metricobserver.Source
	metrics *live.Metrics
	nodeUID string
}

func (s livePowerSource) ReadMetrics(ctx context.Context) (metricobserver.Reading, error) {
	reading, err := s.Source.ReadMetrics(ctx)
	var watts *float64
	if err == nil {
		watts = &reading.Watts
	}
	s.metrics.Power(s.nodeUID, time.Now().UTC(), watts)
	return reading, err
}
