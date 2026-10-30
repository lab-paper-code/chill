package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

type rawHTTPArchive struct {
	settingsArchive
	objects   []evidencestore.ObjectIdentity
	body      []byte
	list      func(context.Context) error
	openError error
}

func (a *rawHTTPArchive) List(ctx context.Context, _ string) ([]evidencestore.ObjectIdentity, error) {
	if a.list != nil {
		if err := a.list(ctx); err != nil {
			return nil, err
		}
	}
	return a.objects, nil
}
func (a *rawHTTPArchive) Open(context.Context, evidencestore.ObjectIdentity) (io.ReadCloser, error) {
	if a.openError != nil {
		return nil, a.openError
	}
	return io.NopCloser(bytes.NewReader(a.body)), nil
}

func rawHTTPHandler(t *testing.T, archive *rawHTTPArchive, allowed bool) *Handler {
	t.Helper()
	archive.settingsArchive.directory = t.TempDir()
	log, err := serving.NewObservationLog(serving.ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour, Writer: archive})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	kube := fake.NewSimpleClientset()
	kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{Authenticated: true, User: authenticationv1.UserInfo{Username: "collector"}}}, nil
	})
	kube.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		sar := action.(ktesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		if sar.Spec.NonResourceAttributes == nil || !strings.HasPrefix(sar.Spec.NonResourceAttributes.Path, "/chill/api/v1/observations/") {
			t.Errorf("wrong raw SAR: %+v", sar.Spec)
		}
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: allowed}}, nil
	})
	h := &Handler{observationLog: log, authorizer: &servingkube.Authorizer{Client: kube}, mux: http.NewServeMux(), config: HandlerConfig{RequestTimeout: 50 * time.Millisecond, OperationTimeout: 200 * time.Millisecond, MaxQueryEvents: 10}}
	h.mux.HandleFunc("GET /api/v1/observations/scheduler", h.schedulerObservations)
	return h
}

func rawHTTPRequest(handler http.Handler, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Authorization", "Bearer collector-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestRawHTTPStrictPathsAndAuthorization(t *testing.T) {
	h := rawHTTPHandler(t, &rawHTTPArchive{}, true)
	for _, path := range []string{
		rawObservationsPath, rawObservationsPath + "?hour=20261030T10&hour=20261030T11",
		rawObservationsPath + "?hour=20261030T10&kind=requests", rawObservationsPath + "?hour=20261030T24",
		rawObservationsPath + "/20261030T10/bad", rawObservationsPath + "/20261030T10/requests/extra",
		rawObservationsPath + "/definitions?extra=1", rawObservationsPath + "/../definitions",
		rawObservationsPath + "/20261030T10%2Frequests/requests", rawObservationsPath + "/",
	} {
		if response := rawHTTPRequest(h, path); response.Code != http.StatusBadRequest {
			t.Fatalf("unsafe raw request %s: %d %s", path, response.Code, response.Body)
		}
	}
	if response := rawHTTPRequest(h, rawObservationsPath+"/20000101T00/requests"); response.Code != http.StatusNotFound {
		t.Fatalf("missing stream: %d %s", response.Code, response.Body)
	}
	if response := rawHTTPRequest(h, rawObservationsPath+"/"+time.Now().UTC().Format("20060102T15")+"/requests"); response.Code != http.StatusConflict {
		t.Fatalf("open stream: %d %s", response.Code, response.Body)
	}
	request := httptest.NewRequest(http.MethodGet, rawObservationsPath+"/definitions", nil)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("raw bypassed bearer auth: %d", response.Code)
	}
	denied := rawHTTPHandler(t, &rawHTTPArchive{}, false)
	if response := rawHTTPRequest(denied, rawObservationsPath+"/definitions"); response.Code != http.StatusForbidden {
		t.Fatalf("raw bypassed SAR: %d", response.Code)
	}
}

func TestRawHTTPStoredGzipAndTransferFailure(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write([]byte("{\"preserve\": true}\n"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	hour := time.Now().UTC().Add(-4 * time.Hour).Format("20060102T15")
	archive := &rawHTTPArchive{body: compressed.Bytes(), objects: []evidencestore.ObjectIdentity{{Store: "evidence", Key: "serving/observations.v1alpha5/" + hour + "/scheduler.jsonl.gz", Size: int64(compressed.Len())}}}
	h := rawHTTPHandler(t, archive, true)
	response := rawHTTPRequest(h, rawObservationsPath+"/"+hour+"/scheduler")
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/gzip" || response.Header().Get("Content-Encoding") != "" || response.Header().Get("Cache-Control") != "no-store" || !bytes.Equal(response.Body.Bytes(), compressed.Bytes()) {
		t.Fatalf("gzip changed: %d %v %q", response.Code, response.Header(), response.Body.Bytes())
	}
	archive.openError = errors.New("store failed")
	if response := rawHTTPRequest(h, rawObservationsPath+"/"+hour+"/scheduler"); response.Code < 500 {
		t.Fatalf("store error hidden: %d", response.Code)
	}
	archive.openError = nil
	archive.body = archive.body[:len(archive.body)-1]
	server := httptest.NewServer(h)
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL+rawObservationsPath+"/"+hour+"/scheduler", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer collector-token")
	result, err := server.Client().Do(request)
	if err == nil {
		_, readErr := io.ReadAll(result.Body)
		_ = result.Body.Close()
		if readErr == nil {
			t.Fatal("truncated gzip transfer looked complete")
		}
	}
}

func TestRawHTTPAndSchedulerSeparateProcessingAndWriteDeadlines(t *testing.T) {
	for _, path := range []string{rawObservationsPath + "?hour=20000101T00", "/api/v1/observations/scheduler?from=2000-01-01T00:00:00Z&to=2000-01-01T01:00:00Z"} {
		t.Run(path, func(t *testing.T) {
			archive := &rawHTTPArchive{}
			h := rawHTTPHandler(t, archive, true)
			archive.list = func(ctx context.Context) error {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) <= h.config.RequestTimeout {
					t.Errorf("historical read used short request deadline")
				}
				<-ctx.Done()
				return ctx.Err()
			}
			server := httptest.NewServer(h)
			defer server.Close()
			request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer collector-token")
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := response.Body.Close(); err != nil {
					t.Error(err)
				}
			}()
			var body serving.Error
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil || response.StatusCode != http.StatusGatewayTimeout || body.Code != requestTimeoutCode {
				t.Fatalf("processing timeout could not be delivered: %d %+v %v", response.StatusCode, body, err)
			}
		})
	}
}

func TestRawHTTPEmptyDefinitions(t *testing.T) {
	archive := &rawHTTPArchive{}
	h := rawHTTPHandler(t, archive, true)
	response := rawHTTPRequest(h, rawObservationsPath+"/definitions")
	if response.Code != http.StatusOK || response.Body.Len() != 0 || response.Header().Get("Content-Length") != "0" || response.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("empty definitions: %d %v %q", response.Code, response.Header(), response.Body.String())
	}
}
