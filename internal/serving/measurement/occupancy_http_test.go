package measurement_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving/measurement"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/management"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	authentication "k8s.io/api/authentication/v1"
	authorization "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
)

func TestOccupancyLocalHTTPSDryRunThenCreate(t *testing.T) {
	service, kube, creates := measurement.OccupancyHTTPFixture(t)
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
	log, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence", Retention: time.Hour,
		Writer: testArchive{directory: t.TempDir()},
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
		BuildOccupancy:  service.BuildOccupancy,
		SubmitOccupancy: service.SubmitOccupancy, DeleteOccupancy: service.DeleteOccupancy,
	}
	handler, err := httpapi.NewHandler(&management.Manager{}, &serving.Runtime{},
		&servingkube.Authorizer{Client: kube}, log, &servingkube.Observer{}, config)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	endpoint := server.URL + "/api/v1/namespaces/allowed/dispatch-occupancy-measurements"
	body := `{"node":"node","batchEnergyOptimization":"beo","workload":"workload","batch":2}`
	var dryResult measurement.OccupancyMutationResult
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
		var result measurement.OccupancyMutationResult
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
		if result.Task.Spec.Workers != 8 || result.Task.Spec.WarmupSeconds != 5 ||
			result.Task.Spec.DurationSeconds != 15 || result.Task.Spec.MinimumPairs != 100 {
			t.Fatal("POST did not preserve the configured short burst")
		}
		if dry {
			dryResult = result
			if *creates != 0 || result.Request != "NOT_SUBMITTED" {
				t.Fatal("dry-run created a native measurement")
			}
		} else if *creates != 1 || result.Request != "Accepted" ||
			!reflect.DeepEqual(dryResult.Task.Spec, result.Task.Spec) {
			t.Fatal("real POST changed the frozen dry-run object")
		}
	}
}

type testArchive struct{ directory string }

func (a testArchive) StreamImmutable(
	_ context.Context, location evidencestore.ObjectLocation, produce func(io.Writer) error,
) (evidencestore.Receipt, error) {
	output := filepath.Join(a.directory, location.Key)
	if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		return evidencestore.Receipt{}, err
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	err = produce(file)
	info, statErr := file.Stat()
	err = errors.Join(err, statErr, file.Sync(), file.Close())
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	identity := evidencestore.ObjectIdentity{Store: location.Store, Key: location.Key, Size: info.Size()}
	return evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion,
		Object:        identity, Outcome: evidencestore.CreateOutcomeCreated,
	}, nil
}
