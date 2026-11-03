package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	authentication "k8s.io/api/authentication/v1"
	authorization "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestOccupancyLocalHTTPSDryRunThenCreate(t *testing.T) {
	service, _, creates := occupancyTestService(t)
	kube := service.kube.(*kubefake.Clientset)
	kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authentication.TokenReview{Status: authentication.TokenReviewStatus{
			Authenticated: true, User: authentication.UserInfo{Username: "operator"},
		}}, nil
	})
	kube.PrependReactor("create", "subjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authorization.SubjectAccessReview{
			Status: authorization.SubjectAccessReviewStatus{Allowed: true},
		}, nil
	})
	log, err := serving.NewObservationLog(serving.ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence", Retention: time.Hour,
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
	config := httpapi.HandlerConfig{
		RequestTimeout: time.Second, OperationTimeout: time.Second, MaxRequestBytes: 1 << 20, MaxQueryEvents: 10,
		BuildOccupancy:  service.buildOccupancy,
		SubmitOccupancy: service.submitOccupancy, DeleteOccupancy: service.deleteOccupancy,
	}
	handler, err := httpapi.NewHandler(&servingkube.Manager{}, &serving.Runtime{},
		&servingkube.Authorizer{Client: kube}, log, &servingkube.Observer{}, config)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	endpoint := server.URL + "/api/v1/namespaces/allowed/dispatch-occupancy-measurements"
	body := `{"node":"node","batchEnergyOptimization":"beo","workload":"workload","batch":2}`
	var dryResult httpapi.OccupancyMutationResult
	for _, dry := range []bool{true, false} {
		suffix := ""
		status := http.StatusAccepted
		if dry {
			suffix = "?dryRun=All"
			status = http.StatusOK
		}
		req, err := http.NewRequest(http.MethodPost, endpoint+suffix, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer local-smoke")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var result httpapi.OccupancyMutationResult
		decodeErr := json.NewDecoder(response.Body).Decode(&result)
		closeErr := response.Body.Close()
		if response.StatusCode != status || decodeErr != nil || closeErr != nil {
			t.Fatalf("dry=%v status=%d decode=%v close=%v", dry, response.StatusCode, decodeErr, closeErr)
		}
		if result.Task == nil {
			t.Fatal("native task absent")
		}
		if err := result.Task.Spec.ValidateConditions(); err != nil {
			t.Fatal(err)
		}
		if dry {
			dryResult = result
			if *creates != 0 || result.Request != measurementRequestNotSubmitted {
				t.Fatal("dry-run created a native measurement")
			}
		} else if *creates != 1 || result.Request != measurementRequestAccepted ||
			!reflect.DeepEqual(dryResult.Task.Spec, result.Task.Spec) {
			t.Fatal("real POST changed the frozen dry-run object")
		}
	}
}
