package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSettingsPutPreservesDispatchOccupancyBlock(t *testing.T) {
	h, _ := occupancyHandler(t, "")
	h.config.MaxRequestBytes = 1 << 20
	before := schedulerSettingsForTest()
	service, execution, _ := settingsServiceForTest(t, h.authorizer.Client, h.observationLog, before, false, false)
	controller := service.Controller
	raw, err := os.ReadFile("../../../config/samples/serving-scheduler.json")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]json.RawMessage
	if err = json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	var policyBefore any
	if err = json.Unmarshal(config["dispatchOccupancy"], &policyBefore); err != nil {
		t.Fatal(err)
	}
	config["settings"], err = json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	cm := &core.ConfigMap{ObjectMeta: meta.ObjectMeta{Namespace: "serving", Name: "scheduler", ResourceVersion: "1"},
		Data: map[string]string{"scheduler.json": string(raw)}}
	if _, err = h.authorizer.Client.CoreV1().ConfigMaps("serving").Create(context.Background(), cm, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	h.runtime = execution
	h.config.Scheduler = controller
	h.config.SchedulerNamespace = "serving"
	h.config.SchedulerConfigMap = "scheduler"
	h.config.SchedulerConfigKey = "scheduler.json"
	h.config.SchedulerLock = &service.Lock
	h.config.ChangeSchedulerSettings = service.ChangeSchedulerSettings
	after := before
	after.MaxInstances++
	body, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/scheduler/settings", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer caller-token")
	req.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	h.scheduler(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("settings PUT: %d %s", response.Code, response.Body.String())
	}
	persisted, err := h.authorizer.Client.CoreV1().
		ConfigMaps("serving").
		Get(context.Background(), "scheduler", meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(persisted.Data["scheduler.json"]), &config); err != nil {
		t.Fatal(err)
	}
	var policyAfter any
	if err = json.Unmarshal(config["dispatchOccupancy"], &policyAfter); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(policyBefore, policyAfter) {
		t.Fatal("settings PUT altered dispatchOccupancy")
	}
}
