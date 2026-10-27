package gateway

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving/live"
)

type RouterConfig struct {
	// ControlURL is a fixed wss origin, never a caller-controlled URL.
	ControlURL string
	CAFile     string
	TokenFile  string
	RouterID   string
	Runtime    *serving.Runtime
	Native     *firstfloor.Client
	Recovery   []Recovery
}

// Recovery owns an old, locked observation source solely for reconciliation.
// It never receives a routing snapshot or opens new inference admission.
type Recovery struct {
	RouterID string
	Runtime  *serving.Runtime
}

func (r *Router) recoveryReports() []serving.GatewayReport {
	reports := make([]serving.GatewayReport, 0, len(r.config.Recovery))
	for _, old := range r.config.Recovery {
		reports = append(reports, old.Runtime.GatewayReport(old.RouterID, time.Time{}))
	}
	return reports
}
func (r *Router) owner(id string) *serving.Runtime {
	if id == "" || id == r.config.RouterID {
		return r.config.Runtime
	}
	for _, old := range r.config.Recovery {
		if old.RouterID == id {
			return old.Runtime
		}
	}
	return nil
}
func (r *Router) advanceObservation() error {
	if err := r.config.Runtime.AdvanceGatewayObservation(); err != nil {
		return err
	}
	for _, old := range r.config.Recovery {
		if err := old.Runtime.AdvanceGatewayObservation(); err != nil {
			r.config.Runtime.SetGatewayConnected(false)
			return err
		}
	}
	return nil
}
func (r *Router) sendReport(s *channel, kind string, generation uint64, appliedAt time.Time) error {
	if err := r.advanceObservation(); err != nil {
		return err
	}
	report := r.config.Runtime.GatewayReport(r.config.RouterID, appliedAt)
	return s.send(kind, message{Generation: generation, Report: &report, Recovery: r.recoveryReports()})
}

type Router struct {
	config   RouterConfig
	client   *http.Client
	endpoint string
}

func NewRouter(config RouterConfig) (*Router, error) {
	u, err := url.Parse(config.ControlURL)
	if err != nil || u.Scheme != "wss" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return nil, errors.New("fixed wss control origin without credentials, path, query or fragment required")
	}
	if config.Runtime == nil || config.Native == nil || !validRouterID(config.RouterID) || config.TokenFile == "" || config.CAFile == "" {
		return nil, errors.New("runtime, native client, generation identity, CA and bearer token file required")
	}
	for _, old := range config.Recovery {
		if old.Runtime == nil || old.RouterID == config.RouterID || !samePodGeneration(old.RouterID, config.RouterID) {
			return nil, errors.New("recovery requires a distinct locked generation of the same Pod UID")
		}
		old.Runtime.SetGatewayConnected(false)
	}
	raw, err := os.ReadFile(config.CAFile)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(raw) {
		return nil, errors.New("control CA contains no certificates")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: u.Hostname()}
	transport.Proxy = nil
	u.Path = Path
	config.Runtime.SetGatewayConnected(false)
	return &Router{config: config, endpoint: u.String(), client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("control redirects forbidden") }}}, nil
}

// Run reconnects by full baseline, not replaying ACKs from an obsolete connection.
// Existing native work remains in Runtime and is included in every rejoin report.
func (r *Router) Run(ctx context.Context) error {
	defer r.config.Runtime.SetGatewayConnected(false)
	for {
		if ctx.Err() != nil {
			return nil
		}
		_ = r.session(ctx)
		r.config.Runtime.SetGatewayConnected(false)
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
func (r *Router) session(ctx context.Context) error {
	token, err := os.ReadFile(r.config.TokenFile)
	if err != nil {
		return err
	}
	bearer := strings.TrimSpace(string(token))
	if bearer == "" || strings.ContainsAny(bearer, " \t\r\n") {
		return errors.New("invalid router bearer token file")
	}
	handshake, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, _, err := websocket.Dial(handshake, r.endpoint, &websocket.DialOptions{HTTPClient: r.client, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + bearer}}})
	cancel()
	if err != nil {
		return err
	}
	s := newChannel(ctx, conn)
	watchDone := make(chan struct{})
	defer func() {
		s.close()
		<-watchDone
		r.config.Runtime.SetGatewayFreshUntil(time.Now())
		r.config.Runtime.SetGatewayConnected(false)
	}()
	r.config.Runtime.SetGatewayFreshUntil(time.Unix(0, s.lastRead.Load()).Add(FreshnessTimeout))
	go s.writeLoop()
	go func() { defer close(watchDone); s.watchFreshness(r.config.Runtime) }()
	if err = r.advanceObservation(); err != nil {
		return err
	}
	if err = s.send("resume", message{RouterID: r.config.RouterID, Recovery: r.recoveryReports()}); err != nil {
		return err
	}
	var generation uint64
	var appliedAt time.Time
	var version string
	reportTicker := time.NewTicker(time.Second)
	defer reportTicker.Stop()
	done := make(chan struct{})
	defer close(done)
	var stateMu sync.Mutex
	go func() {
		for {
			select {
			case <-done:
				return
			case <-s.ctx.Done():
				return
			case <-reportTicker.C:
				stateMu.Lock()
				var reportErr error
				if generation != 0 && version != "" {
					reportErr = r.sendReport(s, "report", generation, appliedAt)
				}
				stateMu.Unlock()
				if reportErr != nil {
					s.cancel()
					return
				}
			}
		}
	}()
	for {
		m, err := s.read()
		if err != nil {
			return err
		}
		if s.ctx.Err() != nil {
			return s.ctx.Err()
		}
		r.config.Runtime.SetGatewayFreshUntil(m.receivedAt.Add(FreshnessTimeout))
		if s.ctx.Err() != nil || s.lastRead.Load() < 0 {
			r.config.Runtime.SetGatewayFreshUntil(time.Now())
			return context.Canceled
		}
		stateMu.Lock()
		err = func() error {
			if s.ctx.Err() != nil {
				return s.ctx.Err()
			}
			switch m.Type {
			case "plan":
				if m.Plan == nil || m.Generation == 0 || (generation != 0 && m.Generation != generation) {
					return live.ErrProtocol
				}
				r.config.Runtime.SetGatewayConnected(false)
				targets, err := r.targets(*m.Plan)
				if err != nil {
					return err
				}
				if err = r.config.Runtime.ApplyGatewayPlan(*m.Plan, targets); err != nil {
					return err
				}
				generation = m.Generation
				version = m.Plan.PlanVersion
				appliedAt = time.Now().UTC()
				return r.sendReport(s, "applied", generation, appliedAt)
			case "ready":
				if m.Generation != generation || m.Plan == nil || m.Plan.PlanVersion != version {
					return live.ErrProtocol
				}
				if err := r.advanceObservation(); err != nil {
					return err
				}
				if s.ctx.Err() != nil {
					return s.ctx.Err()
				}
				r.config.Runtime.SetGatewayConnected(true)
			case "heartbeat":
				if generation != 0 && m.Generation != generation {
					return live.ErrProtocol
				}
				return s.send("heartbeat", message{Generation: m.Generation})
			case "progress":
				if m.Generation != generation || m.Progress == nil {
					return live.ErrProtocol
				}
				owner := r.owner(m.Owner)
				if owner == nil {
					return live.ErrProtocol
				}
				return owner.ObserveBackend(m.Instance, *m.Progress)
			case "state":
				if m.Generation != generation || m.State == nil {
					return live.ErrProtocol
				}
				owner := r.owner(m.Owner)
				if owner == nil {
					return live.ErrProtocol
				}
				return owner.ObserveGatewayInstance(*m.State)
			default:
				return live.ErrProtocol
			}
			return nil
		}()
		stateMu.Unlock()
		if err != nil {
			return err
		}
	}
}
func (r *Router) targets(plan serving.GatewayPlan) ([]serving.ReadyInstance, error) {
	targets := make([]serving.ReadyInstance, 0, len(plan.Targets))
	for _, t := range plan.Targets {
		if t.Profile == nil || t.RouterLimit < 0 || plan.Participants < 1 || t.RouterLimit != t.Capacity/plan.Participants {
			return nil, live.ErrProtocol
		}
		routing := map[string]int32{}
		var weight int32 = 1
		for uid, weights := range plan.Routing {
			if w := weights[t.InstanceID]; w > 0 {
				routing[uid] = w
				weight = w
			}
		}
		spec := firstfloor.Spec{Endpoint: t.Endpoint, ModelName: t.ModelName, InstanceID: t.InstanceID, Node: t.Node, Profile: t.Profile, Weight: weight, Routing: routing, Capacity: t.Capacity, ExpectedRuntime: t.ExpectedRuntime, DecisionID: t.DecisionID}
		targets = append(targets, serving.ReadyInstance{State: serving.InstanceState{ID: t.InstanceID, PodUID: t.PodUID, ContainerID: t.ContainerID, Node: t.Node, Profile: t.Profile.ID, Ready: true, ObservedAt: time.Now().UTC(), OwnerWorkloadUID: t.OwnerWorkloadUID}, Profile: t.Profile, Routing: routing, Capacity: t.RouterLimit, OwnerWorkloadUID: t.OwnerWorkloadUID, Execute: func(ctx context.Context, d serving.Dispatch) (serving.BackendResult, error) {
			return r.config.Native.Execute(ctx, spec, d)
		}})
	}
	return targets, nil
}
