package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/live"
)

// AuthorizationError lets the composition root preserve the existing review
// boundary's HTTP classification without importing Kubernetes into this package.
type AuthorizationError struct{ Status int }

func (e *AuthorizationError) Error() string { return "internal gateway authorization failed" }

type RouterState struct {
	RouterID             string                `json:"routerID"`
	Generation           uint64                `json:"generation"`
	MembershipGeneration uint64                `json:"membershipGeneration"`
	Connected            bool                  `json:"connected"`
	JoinedAt             time.Time             `json:"joinedAt"`
	LeftAt               time.Time             `json:"leftAt"`
	LastSeen             time.Time             `json:"lastSeen"`
	Report               serving.GatewayReport `json:"report"`
	// Exposed survives disconnect and restart. A missing report is not zero.
	Exposed           map[string]bool               `json:"exposed"`
	Sources           map[string]Source             `json:"sources,omitempty"`
	Pending           []Evidence                    `json:"pending,omitempty"`
	Custodian         string                        `json:"custodian,omitempty"`
	CustodyGeneration uint64                        `json:"custodyGeneration,omitempty"`
	Definitions       map[string][]DefinitionPeriod `json:"definitions,omitempty"`
	ArrivalWindow     time.Duration                 `json:"arrivalWindowNanos,omitempty"`
}
type Evidence struct {
	Instance   string                  `json:"instance"`
	Progress   serving.BackendProgress `json:"progress"`
	State      *serving.InstanceState  `json:"state,omitempty"`
	Generation uint64                  `json:"generation"`
	Seq        uint64                  `json:"seq"`
}

func reportOwns(report serving.GatewayReport, instance, id string) bool {
	for _, o := range report.Outstanding {
		if o.InstanceID == instance {
			for _, request := range o.RequestIDs {
				if request == id {
					return true
				}
			}
		}
	}
	for _, r := range report.Requests {
		if r.ID == id && r.Instance == instance {
			return true
		}
	}
	return false
}
func reflectCompletion(report *serving.GatewayReport, instance string, p serving.BackendProgress) {
	if !p.ExecutionComplete || p.Phase != "finished" {
		return
	}
	for i := range report.Outstanding {
		o := &report.Outstanding[i]
		if o.InstanceID == instance {
			for j, id := range o.RequestIDs {
				if id == p.RequestID {
					o.RequestIDs = append(o.RequestIDs[:j], o.RequestIDs[j+1:]...)
					o.Count--
					break
				}
			}
		}
	}
	for i := len(report.Requests) - 1; i >= 0; i-- {
		if report.Requests[i].ID == p.RequestID && report.Requests[i].Instance == instance {
			report.Requests = append(report.Requests[:i], report.Requests[i+1:]...)
		}
	}
}

type Source struct {
	NodeUID   string          `json:"nodeUID"`
	Workloads map[string]bool `json:"workloads"`
}

func planSource(plan *serving.GatewayPlan, t serving.GatewayTarget) Source {
	source := Source{NodeUID: t.Node.UID, Workloads: map[string]bool{}}
	if t.OwnerWorkloadUID != "" {
		source.Workloads[t.OwnerWorkloadUID] = true
	}
	for uid, weights := range plan.Routing {
		if weights[t.InstanceID] > 0 {
			source.Workloads[uid] = true
		}
	}
	return source
}
func sourceMatches(source Source, uid, node string) bool {
	// Unknown saved scope remains conservative rather than inventing coverage.
	return (node == "" || source.NodeUID == "" || source.NodeUID == node) && (uid == "" || len(source.Workloads) == 0 || source.Workloads[uid])
}

type member struct {
	RouterState
	channel *channel
}
type Control struct {
	mu                 sync.Mutex
	participants       int
	plan               *serving.GatewayPlan
	versions           map[string]bool
	members            map[string]*member
	generation         uint64
	changed            chan struct{}
	closed             bool
	membershipFile     string
	failure            error
	membershipRecorder func(RouterState) error
}

func NewControl(participants int) (*Control, error) {
	if participants < 1 {
		return nil, errors.New("positive fixed router count required")
	}
	return &Control{participants: participants, versions: map[string]bool{}, members: map[string]*member{}, changed: make(chan struct{})}, nil
}
func (c *Control) signalLocked() { close(c.changed); c.changed = make(chan struct{}) }

// SetMembershipRecorder installs the durable control observation callback.
// The callback must not reenter Control.
func (c *Control) SetMembershipRecorder(record func(RouterState) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.membershipRecorder = record
}
func (c *Control) SetMembershipFile(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(c.members) != 0 {
		return errors.New("clean absolute membership file required before joins")
	}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		var states []RouterState
		if err = json.Unmarshal(raw, &states); err != nil {
			return err
		}
		if err = c.restoreLocked(states); err != nil {
			return err
		}
	}
	c.membershipFile = path
	return c.persistLocked()
}
func (c *Control) RestoreMembership(states []RouterState) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.restoreLocked(states)
}
func (c *Control) restoreLocked(states []RouterState) error {
	if len(c.members) != 0 {
		return errors.New("membership already initialized")
	}
	for _, state := range states {
		if !validRouterID(state.RouterID) || c.members[state.RouterID] != nil {
			return errors.New("invalid saved router identity")
		}
		state.Connected = false
		if state.Exposed == nil {
			state.Exposed = map[string]bool{}
		}
		c.members[state.RouterID] = &member{RouterState: state}
		if state.Generation > c.generation {
			c.generation = state.Generation
		}
	}
	return nil
}
func (c *Control) statesLocked() []RouterState {
	out := make([]RouterState, 0, len(c.members))
	for _, m := range c.members {
		out = append(out, m.RouterState)
	}
	return out
}
func (c *Control) persistLocked() (result error) {
	if c.membershipFile == "" {
		return nil
	}
	raw, err := json.Marshal(c.statesLocked())
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(c.membershipFile), ".gateway-membership-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() {
		if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(name, c.membershipFile); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
func (c *Control) failLocked(err error) error {
	c.failure = err
	for _, m := range c.members {
		if m.channel != nil {
			m.channel.cancel()
		}
	}
	c.signalLocked()
	return err
}

// Publish accepts only the parent's native-ACK-validated physical target set.
// N is fixed for this control lifetime; integer division never rounds zero up.
func (c *Control) Publish(plan serving.GatewayPlan) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.failure != nil {
		return live.ErrClosed
	}
	if plan.PlanVersion == "" || plan.CreatedAt.IsZero() || plan.Participants != c.participants || c.versions[plan.PlanVersion] {
		return errors.New("unique version and fixed participant count required")
	}
	seen := map[string]bool{}
	for _, t := range plan.Targets {
		if t.PodUID == "" || t.ContainerID == "" || t.InstanceID != t.PodUID+"/"+t.ContainerID || t.DecisionID == "" || t.Capacity < 1 || seen[t.InstanceID] {
			return errors.New("exact native-ACK target identity and capacity required")
		}
		seen[t.InstanceID] = true
	}
	// Freeze caller-owned maps, profiles and definitions at the boundary.
	raw, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	var frozen serving.GatewayPlan
	if err = json.Unmarshal(raw, &frozen); err != nil {
		return err
	}
	for i := range frozen.Targets {
		frozen.Targets[i].RouterLimit = frozen.Targets[i].Capacity / c.participants
	}
	if _, err = live.Encode("plan", 1, time.Now(), struct {
		Plan *serving.GatewayPlan `json:"plan"`
	}{&frozen}); err != nil {
		return err
	}
	c.plan = &frozen
	c.versions[plan.PlanVersion] = true
	for _, m := range c.members {
		if m.Connected {
			if m.Sources == nil {
				m.Sources = map[string]Source{}
			}
			for _, t := range frozen.Targets {
				m.Exposed[t.InstanceID] = true
				m.Sources[t.InstanceID] = planSource(&frozen, t)
			}
		}
	}
	if err = c.persistLocked(); err != nil {
		return c.failLocked(err)
	}
	for _, m := range c.members {
		if m.Connected {
			_ = m.channel.send("plan", message{Generation: m.Generation, Plan: c.plan})
		}
	}
	c.signalLocked()
	return nil
}
func (c *Control) Handler(authorize func(*http.Request) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != Path || r.URL.RawQuery != "" || r.URL.ForceQuery || len(r.Header.Values("Sec-WebSocket-Protocol")) != 0 {
			http.Error(w, "exact internal gateway path required", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.TLS == nil {
			http.Error(w, "TLS required", http.StatusBadRequest)
			return
		}
		if authorize == nil {
			http.Error(w, "authorization unavailable", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		err := authorize(r.WithContext(ctx))
		if ctx.Err() != nil {
			err = &AuthorizationError{Status: http.StatusServiceUnavailable}
		}
		cancel()
		if err != nil {
			status := http.StatusServiceUnavailable
			var denied *AuthorizationError
			if errors.As(err, &denied) && (denied.Status == http.StatusUnauthorized || denied.Status == http.StatusForbidden) {
				status = denied.Status
			}
			http.Error(w, "internal gateway authorization failed", status)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		s := newChannel(r.Context(), conn)
		defer s.close()
		go s.writeLoop()
		hello, err := s.read()
		if err != nil || hello.Type != "resume" || !validRouterID(hello.RouterID) {
			return
		}
		m, err := c.joinRecovery(hello.RouterID, s, hello.Recovery)
		if err != nil {
			return
		}
		defer c.leave(m, s)
		ticker := time.NewTicker(HeartbeatInterval)
		defer ticker.Stop()
		go func() {
			for {
				select {
				case <-s.ctx.Done():
					return
				case <-ticker.C:
					if s.send("heartbeat", message{Generation: m.Generation}) != nil {
						return
					}
				}
			}
		}()
		for {
			msg, err := s.read()
			if err != nil {
				return
			}
			if err = c.accept(m, s, msg); err != nil {
				return
			}
		}
	})
}
func (c *Control) join(id string, s *channel) (*member, error) {
	return c.joinRecovery(id, s, nil)
}
func (c *Control) joinRecovery(id string, s *channel, recovery []serving.GatewayReport) (*member, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.validateJoinLocked(id, recovery); err != nil {
		return nil, err
	}
	old := c.members[id]
	if old != nil && old.channel != nil {
		old.channel.cancel()
	}
	c.generation++
	m := &member{RouterState: RouterState{RouterID: id, Generation: c.generation, MembershipGeneration: c.generation, Connected: true, JoinedAt: time.Now().UTC(), LastSeen: time.Now().UTC(), Exposed: map[string]bool{}}, channel: s}
	if old != nil {
		m.Exposed = old.Exposed
		m.Report = old.Report
		m.JoinedAt = old.JoinedAt
	}
	if old != nil {
		m.Sources = old.Sources
	}
	if m.Sources == nil {
		m.Sources = map[string]Source{}
	}
	if old != nil {
		m.Pending = old.Pending
	}
	if old != nil {
		m.Definitions = old.Definitions
		m.ArrivalWindow = old.ArrivalWindow
	}
	m.Report.AppliedAt = time.Time{}
	if c.plan != nil {
		for _, t := range c.plan.Targets {
			m.Exposed[t.InstanceID] = true
			m.Sources[t.InstanceID] = planSource(c.plan, t)
		}
	}
	c.members[id] = m
	for _, prior := range c.members {
		if prior.Custodian == id {
			prior.CustodyGeneration = m.Generation
		}
	}
	if c.membershipRecorder != nil {
		if err := c.membershipRecorder(m.RouterState); err != nil {
			return nil, c.failLocked(err)
		}
	}
	if err := c.persistLocked(); err != nil {
		return nil, c.failLocked(err)
	}
	c.signalLocked()
	if c.plan != nil {
		if err := s.send("plan", message{Generation: m.Generation, Plan: c.plan}); err != nil {
			return nil, err
		}
	}
	for i := range m.Pending {
		if err := c.sendEvidenceLocked(m, &m.Pending[i]); err != nil {
			return nil, err
		}
	}
	for _, prior := range c.members {
		if prior.Custodian == id {
			for i := range prior.Pending {
				if err := c.sendEvidenceLocked(prior, &prior.Pending[i]); err != nil {
					return nil, err
				}
			}
		}
	}
	return m, nil
}

func (c *Control) validateJoinLocked(id string, recovery []serving.GatewayReport) error {
	if c.closed || c.failure != nil {
		return live.ErrClosed
	}
	if !validRouterID(id) {
		return live.ErrProtocol
	}
	active := 0
	for key, m := range c.members {
		if m.Connected && key != id {
			active++
		}
	}
	if active >= c.participants {
		return errors.New("fixed router membership full")
	}
	if err := c.registerRecoveryLocked(id, recovery); err != nil {
		return err
	}
	if c.members[id] != nil || c.plan == nil {
		return nil
	}
	return c.checkDisconnectedCapacityLocked(id)
}

func (c *Control) checkDisconnectedCapacityLocked(id string) error {
	for _, prior := range c.members {
		if prior.Connected || prior.Custodian == id {
			continue
		}
		for _, target := range c.plan.Targets {
			if prior.Exposed[target.InstanceID] {
				return errors.New("disconnected generation still owns a capacity share")
			}
			for _, o := range prior.Report.Outstanding {
				if o.InstanceID == target.InstanceID && o.Count > 0 {
					return errors.New("old physical reservations still own a capacity share")
				}
			}
		}
	}
	return nil
}
func (c *Control) leave(m *member, s *channel) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.members[m.RouterID] != m || m.channel != s {
		return
	}
	m.Connected = false
	m.LeftAt = time.Now().UTC()
	m.channel = nil
	if c.membershipRecorder != nil {
		if err := c.membershipRecorder(m.RouterState); err != nil {
			_ = c.failLocked(err)
		}
	}
	if err := c.persistLocked(); err != nil {
		_ = c.failLocked(err)
	}
	c.signalLocked()
}
func (c *Control) accept(m *member, s *channel, msg message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.members[m.RouterID] != m || m.channel != s || msg.Generation != m.Generation {
		return live.ErrProtocol
	}
	m.LastSeen = time.Now().UTC()
	if msg.Type == "heartbeat" {
		return nil
	}
	if err := c.validateReportLocked(m, msg); err != nil {
		return err
	}
	report := msg.Report
	c.definitionPeriodsLocked(m, *report)
	if err := c.updateRecoveryLocked(m, msg); err != nil {
		return err
	}
	m.Report = *report
	pending := m.Pending[:0]
	for _, e := range m.Pending {
		if e.Generation == m.Generation && msg.Checkpoint >= e.Seq && (e.State != nil || !reportOwns(*report, e.Instance, e.Progress.RequestID)) {
			continue
		}
		reflectEvidence(&m.Report, e)
		pending = append(pending, e)
	}
	m.Pending = pending
	// This complete post-apply report seals the old assignment gate, including
	// reservations not yet sent. Only then can absence establish a physical zero.
	current := map[string]bool{}
	for _, t := range c.plan.Targets {
		current[t.InstanceID] = true
	}
	for id := range m.Exposed {
		if !current[id] {
			delete(m.Exposed, id)
		}
	}
	if err := c.persistLocked(); err != nil {
		return c.failLocked(err)
	}
	c.signalLocked()
	if c.slotAvailableLocked(m) {
		return s.send("ready", message{Generation: m.Generation, Plan: c.plan})
	}
	return nil
}

func (c *Control) validateReportLocked(m *member, msg message) error {
	if (msg.Type != "applied" && msg.Type != "report") || msg.Report == nil || c.plan == nil {
		return live.ErrProtocol
	}
	report := msg.Report
	if report.RouterID != m.RouterID || report.PlanVersion != c.plan.PlanVersion || report.AppliedAt.IsZero() || report.ObservedAt.IsZero() {
		return live.ErrProtocol
	}
	if msg.Type == "report" && m.Report.AppliedAt.IsZero() {
		return live.ErrProtocol
	}
	if err := validateReservations(report.Outstanding, false); err != nil {
		return err
	}
	return validateArrivals(*report)
}

func validateReservations(outstanding []serving.GatewayOutstanding, requireIdentity bool) error {
	seen := map[string]bool{}
	for _, o := range outstanding {
		if requireIdentity && (o.PodUID == "" || o.ContainerID == "") {
			return live.ErrProtocol
		}
		if o.InstanceID != o.PodUID+"/"+o.ContainerID || seen[o.InstanceID] || o.Count < 0 || o.Count != len(o.RequestIDs) {
			return live.ErrProtocol
		}
		seen[o.InstanceID] = true
		ids := map[string]bool{}
		for _, id := range o.RequestIDs {
			if id == "" || ids[id] {
				return live.ErrProtocol
			}
			ids[id] = true
		}
	}
	return nil
}
func (c *Control) appliedLocked(version string) bool {
	if c.plan == nil || c.plan.PlanVersion != version || c.failure != nil {
		return false
	}
	count := 0
	for _, m := range c.members {
		if m.Connected {
			count++
			if time.Since(m.LastSeen) > FreshnessTimeout || m.Report.PlanVersion != version || m.Report.AppliedAt.IsZero() {
				return false
			}
		}
	}
	return count > 0
}
func (c *Control) WaitApplied(ctx context.Context, version string) error {
	for {
		c.mu.Lock()
		if c.appliedLocked(version) {
			c.mu.Unlock()
			return nil
		}
		if c.closed || c.failure != nil {
			c.mu.Unlock()
			return live.ErrClosed
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
func (c *Control) CanRetire(instance string) bool { return c.Drained("", instance, "") }
func (c *Control) Drained(uid, instance, node string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.plan == nil || !c.appliedLocked(c.plan.PlanVersion) {
		return false
	}
	workloadClosed := uid != "" && instance == "" && node == "" && !c.plan.Admission[uid]
	if !workloadClosed {
		for _, t := range c.plan.Targets {
			if (instance == "" || t.InstanceID == instance) && sourceMatches(planSource(c.plan, t), uid, node) {
				return false
			}
		}
	}
	for _, m := range c.members {
		sealedWorkload := workloadClosed && m.Connected &&
			time.Since(m.LastSeen) <= FreshnessTimeout &&
			m.Report.PlanVersion == c.plan.PlanVersion && !m.Report.AppliedAt.IsZero()
		if !memberDrained(m, uid, instance, node, sealedWorkload) {
			return false
		}
	}
	return true
}

func memberDrained(m *member, uid, instance, node string, sealedWorkload bool) bool {
	if !sealedWorkload {
		for id := range m.Exposed {
			if (instance == "" || id == instance) && sourceMatches(m.Sources[id], uid, node) {
				return false
			}
		}
	}
	for _, o := range m.Report.Outstanding {
		if o.Count > 0 && (instance == "" || o.InstanceID == instance) && sourceMatches(m.Sources[o.InstanceID], uid, node) {
			return false
		}
	}
	for _, r := range m.Report.Requests {
		if (instance == "" || r.Instance == instance) &&
			(uid == "" || r.Definition != nil && r.Definition.UID == uid) &&
			(node == "" || requestNode(m.Report, r.ID, node)) {
			return false
		}
	}
	return true
}
func requestNode(report serving.GatewayReport, id, node string) bool {
	for _, r := range report.Open {
		if r.RequestID == id {
			return r.Node != nil && r.Node.UID == node
		}
	}
	return true
}
func (c *Control) Reports() []RouterState {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, _ := json.Marshal(c.statesLocked())
	var out []RouterState
	_ = json.Unmarshal(raw, &out)
	return out
}
func (c *Control) sendEvidenceLocked(m *member, e *Evidence) error {
	delivery := c.deliveryLocked(m)
	if delivery == nil {
		return nil
	}
	kind := "progress"
	payload := message{Generation: delivery.Generation, Instance: e.Instance, Progress: &e.Progress, Owner: m.RouterID}
	if e.State != nil {
		kind = "state"
		payload.Progress = nil
		payload.State = e.State
	}
	if err := delivery.channel.send(kind, payload); err != nil {
		return err
	}
	delivery.channel.mu.Lock()
	e.Seq = delivery.channel.seq
	delivery.channel.mu.Unlock()
	e.Generation = delivery.Generation
	return nil
}
func (c *Control) ForwardProgress(instance string, p serving.BackendProgress) error {
	if instance == "" || p.RequestID == "" || p.At.IsZero() {
		return live.ErrProtocol
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	owners := make([]*member, 0, len(c.members))
	for _, m := range c.members {
		if !m.Exposed[instance] && !reportOwns(m.Report, instance, p.RequestID) {
			continue
		}
		evidence := Evidence{Instance: instance, Progress: p}
		if err := c.appendEvidenceLocked(m, evidence); err != nil {
			return err
		}
		reflectEvidence(&m.Report, evidence)
		owners = append(owners, m)
	}
	// Persist the evidence before delivery, including evidence whose request has
	// not reached the owner's first complete outstanding report yet.
	if err := c.persistLocked(); err != nil {
		return c.failLocked(err)
	}
	for _, m := range owners {
		if err := c.sendEvidenceLocked(m, &m.Pending[len(m.Pending)-1]); err != nil {
			return err
		}
	}
	c.signalLocked()
	return nil
}
func (c *Control) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for _, m := range c.members {
		if m.channel != nil {
			m.channel.cancel()
		}
	}
	c.signalLocked()
	return nil
}

// ForwardState carries native absence evidence to the runtime that owns the
// transport state. Control cannot turn absence alone into a physical zero.
func (c *Control) ForwardState(state serving.InstanceState) error {
	if state.ID == "" || state.ID != state.PodUID+"/"+state.ContainerID || (!state.Stopped && state.Native == nil) || state.ObservedAt.IsZero() {
		return live.ErrProtocol
	}
	// Freeze observer-owned native slices once; all recipients retain this same
	// immutable evidence until a checkpointed report acknowledges processing.
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	var frozen serving.InstanceState
	if err = json.Unmarshal(raw, &frozen); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	owners := make([]*member, 0, len(c.members))
	for _, m := range c.members {
		owns := m.Exposed[state.ID]
		for _, o := range m.Report.Outstanding {
			if o.InstanceID == state.ID && o.Count > 0 {
				owns = true
			}
		}
		if !owns {
			continue
		}
		evidence := Evidence{Instance: state.ID, State: &frozen}
		if err := c.appendEvidenceLocked(m, evidence); err != nil {
			return err
		}
		reflectEvidence(&m.Report, evidence)
		if state.Stopped {
			delete(m.Exposed, state.ID)
		}
		owners = append(owners, m)
	}
	if err := c.persistLocked(); err != nil {
		return c.failLocked(err)
	}
	for _, m := range owners {
		if err := c.sendEvidenceLocked(m, &m.Pending[len(m.Pending)-1]); err != nil {
			return err
		}
	}
	c.signalLocked()
	return nil
}
func reflectEvidence(report *serving.GatewayReport, e Evidence) {
	if e.State == nil {
		reflectCompletion(report, e.Instance, e.Progress)
		return
	}
	if !e.State.Stopped {
		return
	}
	for i := range report.Outstanding {
		if report.Outstanding[i].InstanceID == e.Instance {
			report.Outstanding[i].Count = 0
			report.Outstanding[i].RequestIDs = nil
		}
	}
	for i := len(report.Requests) - 1; i >= 0; i-- {
		if report.Requests[i].Instance == e.Instance {
			report.Requests = append(report.Requests[:i], report.Requests[i+1:]...)
		}
	}
}
func (c *Control) appendEvidenceLocked(m *member, e Evidence) error {
	if len(m.Pending) >= live.QueueMessages {
		if delivery := c.deliveryLocked(m); delivery != nil {
			delivery.channel.cancel()
		}
		return live.ErrClosed
	}
	candidate := append(m.Pending, e)
	raw, err := json.Marshal(candidate)
	if err != nil {
		return err
	}
	if len(raw) > live.QueueBytes {
		if delivery := c.deliveryLocked(m); delivery != nil {
			delivery.channel.cancel()
		}
		return live.ErrClosed
	}
	m.Pending = candidate
	return nil
}

func (c *Control) PodDrained(podUID string) bool {
	if podUID == "" || strings.Contains(podUID, "/") {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.plan == nil || !c.appliedLocked(c.plan.PlanVersion) {
		return false
	}
	for _, target := range c.plan.Targets {
		if target.PodUID == podUID {
			return false
		}
	}
	for _, owner := range c.members {
		for instance := range owner.Exposed {
			pod, _, ok := strings.Cut(instance, "/")
			if !ok || pod == podUID {
				return false
			}
		}
		for _, o := range owner.Report.Outstanding {
			if o.Count > 0 && (o.PodUID == "" || o.PodUID == podUID) {
				return false
			}
		}
		for _, request := range owner.Report.Requests {
			if pod, _, ok := strings.Cut(request.Instance, "/"); ok && pod == podUID {
				return false
			}
		}
	}
	return true
}
