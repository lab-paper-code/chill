package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/application"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSchedulerPrioritySettingsRoundTrip(t *testing.T) {
	h, _ := occupancyHandler(t, "")
	h.config.MaxRequestBytes = 1 << 20
	before := schedulerSettingsForTest()
	before.CompletionMarginMS = 23
	before.MinimumDemand = map[string]float64{"other-uid": 3}
	raw, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "sloPriorityStrategy")
	delete(legacy, "workloadPriority")
	reserved := map[string]serving.SchedulerAllocation{"other-node": {MemoryBytes: 17}}
	raw, err = json.Marshal(map[string]any{"settings": legacy, "reserved": reserved})
	if err != nil {
		t.Fatal(err)
	}
	cm := &core.ConfigMap{ObjectMeta: meta.ObjectMeta{Namespace: "serving", Name: "scheduler", ResourceVersion: "1"},
		Data: map[string]string{"scheduler.json": string(raw), "other": "preserved"}}
	if _, err := h.authorizer.Client.CoreV1().ConfigMaps("serving").Create(context.Background(), cm, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	service, execution := settingsServiceForTest(t, h.authorizer.Client, h.observationLog, before, false, false)
	h.runtime = execution
	h.config.Scheduler = service.Controller
	h.config.SchedulerNamespace = "serving"
	h.config.SchedulerConfigMap = "scheduler"
	h.config.SchedulerConfigKey = "scheduler.json"
	h.config.ChangeSchedulerSettings = service.ChangeSchedulerSettings
	request := func(method string, body []byte, tag string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1/scheduler/settings", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer caller-token")
		if tag != "" {
			req.Header.Set("If-Match", tag)
		}
		response := httptest.NewRecorder()
		h.scheduler(response, req)
		return response
	}
	assertConsumers := func(want serving.SchedulerSettings) string {
		t.Helper()
		response := request(http.MethodGet, nil, "")
		var shown serving.SchedulerSettings
		if err := json.Unmarshal(response.Body.Bytes(), &shown); err != nil || response.Code != http.StatusOK || !reflect.DeepEqual(shown, want) {
			t.Fatalf("GET settings: status=%d body=%s error=%v", response.Code, response.Body, err)
		}
		stored, err := h.authorizer.Client.CoreV1().ConfigMaps("serving").Get(context.Background(), "scheduler", meta.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		config, err := application.DecodeSchedulerConfiguration(stored.Data["scheduler.json"], 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		accepted, ok := service.AcceptedSettings()
		if !ok || !reflect.DeepEqual(accepted, want) || !reflect.DeepEqual(service.Controller.Settings(), want) || !reflect.DeepEqual(config.Settings, want) {
			t.Fatal("GET, ConfigMap and accepted priority settings diverged")
		}
		if stored.Data["other"] != "preserved" || !reflect.DeepEqual(config.Reserved, reserved) {
			t.Fatal("priority update changed unrelated configuration")
		}
		return response.Header().Get("ETag")
	}
	want := before
	tag := assertConsumers(want)
	for _, step := range []struct {
		patch, strategy string
		priority        []string
	}{
		{`{"sloPriorityStrategy":"workload","workloadPriority":["deleted-uid","unknown-uid","other-uid"]}`, "workload", []string{"deleted-uid", "unknown-uid", "other-uid"}},
		{`{"workloadPriority":["other-uid","deleted-uid","unknown-uid"]}`, "workload", []string{"other-uid", "deleted-uid", "unknown-uid"}},
		{`{"workloadPriority":[]}`, "workload", []string{}},
		{`{"sloPriorityStrategy":"excess"}`, "excess", []string{}},
	} {
		expected := want
		expected.SLOPriorityStrategy, expected.WorkloadPriority = step.strategy, step.priority
		next, err := serving.ValidateSchedulerSettingsPatch(want, []byte(step.patch))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(next, expected) {
			t.Fatalf("priority merge patch changed unrelated settings or lost order: %+v", next)
		}
		body, err := json.Marshal(next)
		if err != nil {
			t.Fatal(err)
		}
		response := request(http.MethodPut, body, tag)
		var change observation.SchedulerSettingsChange
		if err := json.Unmarshal(response.Body.Bytes(), &change); err != nil || response.Code != http.StatusOK || change.AppliedAt == nil ||
			!reflect.DeepEqual(change.Before, want) || !reflect.DeepEqual(change.After, next) {
			t.Fatalf("priority PUT: status=%d body=%s error=%v", response.Code, response.Body, err)
		}
		want = next
		tag = assertConsumers(want)
	}
	for _, invalid := range []string{
		`{"sloPriorityStrategy":"other"}`,
		`{"sloPriorityStrategy":""}`,
		`{"workloadPriority":["same-uid","same-uid"]}`,
		`{"workloadPriority":[""]}`,
	} {
		raw, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		var fields, rejected map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(invalid), &rejected); err != nil {
			t.Fatal(err)
		}
		for key, value := range rejected {
			fields[key] = value
		}
		body, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		response := request(http.MethodPut, body, tag)
		var failure serving.Error
		if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil || response.Code != http.StatusBadRequest || failure.Code != "InvalidBody" {
			t.Fatalf("invalid priority PUT: status=%d body=%s error=%v", response.Code, response.Body, err)
		}
		tag = assertConsumers(want)
	}
}
