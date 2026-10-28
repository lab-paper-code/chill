package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/gateway"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/live"
)

type unreadInference struct{ reads int }

func (b *unreadInference) Read([]byte) (int, error) { b.reads++; return 0, io.EOF }
func (*unreadInference) Close() error               { return nil }

func TestControlDoesNotReceiveInferenceBodyOrPick(t *testing.T) {
	control, err := gateway.NewControl(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := control.Close(); err != nil {
			t.Error(err)
		}
	})
	calls := 0
	s := &splitControlService{control: control}
	h := s.handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}), &servingkube.Authorizer{})
	body := &unreadInference{}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/workloads/model/infer", nil)
	r.Body = body
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound || calls != 0 || body.reads != 0 {
		t.Fatalf("control became an inference hop: status=%d next=%d reads=%d", w.Code, calls, body.reads)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/workloads", nil))
	if w.Code != http.StatusNoContent || calls != 1 {
		t.Fatal("control management boundary was removed")
	}
	request := httptest.NewRequest(http.MethodGet, gateway.Path, nil)
	request.TLS = &tls.ConnectionState{}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing internal identity was not denied before upgrade: %d", w.Code)
	}
}

func TestControlIsASeparateBuildWithoutRolloutFlag(t *testing.T) {
	var o options
	f := flag.NewFlagSet("role", flag.ContinueOnError)
	o.bindFlags(f)
	if f.Lookup("gateway-enabled") != nil || f.Lookup("gateway-control") != nil {
		t.Fatal("split was wired through a default-off rollout switch")
	}
	if (f.Lookup("router-count") != nil) != (serverRole == controlRole) {
		t.Fatal("split flags escaped their explicit binary composition")
	}
}

func TestRouterMembershipUsesControlObservationStream(t *testing.T) {
	log, err := serving.NewObservationLog(serving.ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour,
		Writer: schedulerPowerArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	at := time.Now().UTC()
	s := &splitControlService{observations: log}
	if err := s.recordMembership(gateway.RouterState{
		RouterID: "pod-generation", Generation: 2, MembershipGeneration: 2, Connected: true, JoinedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, err := log.SchedulerObservationPage(
		context.Background(), at.Add(-time.Second), at.Add(time.Second), 10, 0,
	)
	if err != nil || len(rows) != 1 || rows[0].Action == nil ||
		rows[0].Action.Kind != "gateway-router-membership" ||
		!strings.Contains(string(rows[0].Action.Actual), `"routerID":"pod-generation"`) {
		t.Fatalf("router source missing from control audit: %+v %v", rows, err)
	}
}

func TestControlUsesCommonReportedRequestPrefix(t *testing.T) {
	control, err := gateway.NewControl(2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := control.Close(); err != nil {
			t.Error(err)
		}
	})
	now := time.Now().UTC().Truncate(time.Second).Add(900 * time.Millisecond)
	if err := control.RestoreMembership([]gateway.RouterState{
		{
			RouterID: "one", Generation: 1, JoinedAt: now.Add(-time.Minute),
			Report: serving.GatewayReport{RouterID: "one", ObservedAt: now.Add(-100 * time.Millisecond)},
		},
		{
			RouterID: "two", Generation: 2, JoinedAt: now.Add(-time.Minute),
			Report: serving.GatewayReport{RouterID: "two", ObservedAt: now.Add(-1200 * time.Millisecond)},
		},
	}); err != nil {
		t.Fatal(err)
	}
	s := &splitControlService{control: control}
	got, err := s.observationEnd(now)
	want := now.Add(-1200 * time.Millisecond).Truncate(time.Second)
	if err != nil || !got.Equal(want) {
		t.Fatalf("unreported router tail entered demand window: %v %v, want %v", got, err, want)
	}
	if _, err := s.observationEnd(now.Add(gateway.FreshnessTimeout)); err == nil {
		t.Fatal("stale reports became observed zero demand")
	}
}

func TestClosedEvidenceChannelDoesNotStopIndependentObservation(t *testing.T) {
	control, err := gateway.NewControl(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := control.Close(); err != nil {
			t.Error(err)
		}
	})
	s := &splitControlService{control: control}
	if err := s.deliveryResult(live.ErrClosed); err != nil {
		t.Fatal(err)
	}
	if control.CanRetire("unconfirmed/container") {
		t.Fatal("delivery failure synthesized a physical zero")
	}
	invalid := errors.New("invalid physical evidence")
	if !errors.Is(s.deliveryResult(invalid), invalid) {
		t.Fatal("unexpected evidence failure was hidden")
	}
}
