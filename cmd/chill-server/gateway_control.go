package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/gateway"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/live"
	ctrl "sigs.k8s.io/controller-runtime"
)

// splitControlService exists only in the separately built control composition.
// It observes runtime changes; request bodies and picking never pass through it.
type splitControlService struct {
	control           *gateway.Control
	runtime           *serving.Runtime
	observer          *servingkube.Observer
	observations      *serving.ObservationLog
	opts              options
	mu                sync.Mutex
	lastVersion       string
	last              *serving.GatewayPlan
	changed           chan struct{}
	observationWindow func() time.Duration
}

func newSplitControl(opts options) (*splitControlService, error) {
	control, err := gateway.NewControl(opts.routerCount)
	if err != nil {
		return nil, err
	}
	if err := control.SetMembershipFile(filepath.Join(opts.observationDirectory, "gateway-membership.json")); err != nil {
		_ = control.Close()
		return nil, err
	}
	return &splitControlService{control: control, opts: opts, changed: make(chan struct{}, 1)}, nil
}
func (s *splitControlService) notify() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}
func (s *splitControlService) sources() []serving.RouterObservationSource {
	sources := s.control.ObservationSources()
	for i := range sources {
		if s.opts.routerBufferRoot != "" {
			sources[i].Directory = filepath.Join(s.opts.routerBufferRoot, sources[i].RouterID)
		}
	}
	return sources
}

// observationEnd excludes the unreported tail of live request memory. A common
// UTC-second prefix also matches the router arrival counter window boundaries.
func (s *splitControlService) observationEnd(now time.Time) (time.Time, error) {
	end := now
	for _, source := range s.control.ObservationSources() {
		if !source.LeftAt.IsZero() {
			continue
		}
		if source.ObservedAt.IsZero() || now.Sub(source.ObservedAt) > gateway.FreshnessTimeout {
			return time.Time{}, &serving.Error{Code: "IncompleteObservation", Message: "router request report is unavailable"}
		}
		if source.ObservedAt.Before(end) {
			end = source.ObservedAt
		}
	}
	return end.UTC().Truncate(time.Second), nil
}
func (s *splitControlService) recordMembership(state gateway.RouterState) error {
	actual, err := json.Marshal(struct {
		RouterID             string    `json:"routerID"`
		Generation           uint64    `json:"generation"`
		MembershipGeneration uint64    `json:"membershipGeneration"`
		JoinedAt             time.Time `json:"joinedAt"`
		LeftAt               time.Time `json:"leftAt"`
	}{state.RouterID, state.Generation, state.MembershipGeneration, state.JoinedAt, state.LeftAt})
	if err != nil {
		return err
	}
	phase := "joined"
	if !state.Connected {
		phase = "excluded"
	}
	return s.observations.SchedulerRecord(serving.SchedulerObservation{At: time.Now().UTC(), Action: &serving.SchedulerActionObservation{
		ID: state.RouterID, Kind: "gateway-router-membership", Phase: phase, StartedAt: state.JoinedAt, Actual: actual,
	}})
}

// publish holds no Runtime/Observer lock while waiting for actual router ACKs.
// Concurrent operation scopes share a full-table publication, not a new global
// physical-drain barrier; retirement is checked separately by Control.Drained.
func (s *splitControlService) publish(ctx context.Context, wait bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	states, physical, routing := s.runtime.Current()
	plan := serving.GatewayPlan{Participants: s.opts.routerCount, Definitions: make([]*serving.Definition, 0, len(states)),
		Targets: s.observer.GatewayTargets(), Routing: map[string]map[string]int32{}, Admission: map[string]bool{}}
	for _, state := range states {
		plan.Definitions = append(plan.Definitions, state.Applied)
		plan.Admission[state.Applied.UID] = state.Admission
	}
	slices.SortFunc(plan.Definitions, func(a, b *serving.Definition) int { return strings.Compare(a.UID, b.UID) })
	plan.ObservationWindow = time.Second
	if s.observationWindow != nil {
		plan.ObservationWindow = s.observationWindow()
	}
	valid := make(map[string]bool, len(plan.Targets))
	for i := range plan.Targets {
		plan.Targets[i].RouterLimit = plan.Targets[i].Capacity / s.opts.routerCount
		valid[plan.Targets[i].InstanceID] = true
	}
	used := make(map[string]bool, len(plan.Targets))
	for uid, weights := range routing {
		plan.Routing[uid] = map[string]int32{}
		for id, weight := range weights {
			if !valid[id] || weight <= 0 {
				continue
			}
			index := slices.IndexFunc(physical, func(i serving.InstanceState) bool { return i.ID == id })
			if index >= 0 && physical[index].Routable[uid] {
				plan.Routing[uid][id] = weight
				used[id] = true
			}
		}
	}
	// A withdrawn source must disappear from the new table, even while its
	// native actor stays Ready to finish reservations owned by the old table.
	plan.Targets = slices.DeleteFunc(plan.Targets, func(target serving.GatewayTarget) bool { return !used[target.InstanceID] })
	if s.last == nil || !reflect.DeepEqual(plan, *s.last) {
		var version [16]byte
		if _, err := rand.Read(version[:]); err != nil {
			return err
		}
		published := plan
		published.PlanVersion, published.CreatedAt = hex.EncodeToString(version[:]), time.Now().UTC()
		if err := s.control.Publish(published); err != nil {
			return err
		}
		// Keep metadata out of the semantic comparison; a native observation
		// timestamp alone does not republish an unchanged distribution.
		s.last = &plan
		s.lastVersion = published.PlanVersion
	}
	if wait {
		return s.control.WaitApplied(ctx, s.lastVersion)
	}
	return nil
}

func (s *splitControlService) routingApplied(ctx context.Context) error { return s.publish(ctx, true) }
func (s *splitControlService) start(ctx context.Context) error {
	s.control.SetMembershipRecorder(s.recordMembership)
	if err := s.publish(ctx, false); err != nil {
		return err
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.changed:
				callCtx, cancel := context.WithTimeout(ctx, s.opts.requestTimeout)
				err := s.publish(callCtx, false)
				cancel()
				if err != nil && ctx.Err() == nil {
					ctrl.Log.Error(err, "publish acknowledged router distribution")
				}
			}
		}
	}()
	return nil
}

func (s *splitControlService) forwardState(state serving.InstanceState) error {
	if state.ID == "" || state.PodUID == "" || state.ContainerID == "" || (state.Native == nil && !state.Stopped) {
		return nil
	}
	if state.Stopped && state.ObservedAt.IsZero() {
		state.ObservedAt = time.Now().UTC()
	}
	return s.deliveryResult(s.control.ForwardState(state))
}

func (s *splitControlService) forwardProgress(instance string, progress serving.BackendProgress) error {
	return s.deliveryResult(s.control.ForwardProgress(instance, progress))
}

func (s *splitControlService) deliveryResult(err error) error {
	if errors.Is(err, live.ErrClosed) {
		// A bounded channel failure retains unknown ownership and reconnects.
		// It must not turn a slow router into a global native-observation barrier.
		ctrl.Log.Error(err, "router evidence channel requires resynchronization; retirement remains blocked")
		return nil
	}
	return err
}

func (s *splitControlService) handler(next http.Handler, authorizer *servingkube.Authorizer) http.Handler {
	channel := s.control.Handler(func(r *http.Request) error {
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
			return &gateway.AuthorizationError{Status: http.StatusUnauthorized}
		}
		_, err := authorizer.Authorize(r.Context(), strings.TrimSpace(token), r.Method, r.URL.Path)
		if err == nil {
			return nil
		}
		status := http.StatusServiceUnavailable
		if errors.Is(err, servingkube.ErrUnauthenticated) {
			status = http.StatusUnauthorized
		}
		if errors.Is(err, servingkube.ErrForbidden) {
			status = http.StatusForbidden
		}
		return &gateway.AuthorizationError{Status: status}
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == gateway.Path {
			channel.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/workloads/") && strings.HasSuffix(r.URL.Path, "/infer") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
